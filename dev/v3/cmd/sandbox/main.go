// sandbox is a development-only wrapper around system libvirt and SSH.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const name = "gordon-v3-sandbox"
const imageURL = "https://cloud-images.ubuntu.com/releases/resolute/release/ubuntu-26.04-server-cloudimg-amd64.img"
const imageSHA = "8196be9d7958059cb56c6c75c80fdf6cee8a8885bc149ea791d7db1c7ef93035"
const uri = "qemu:///system"

const help = `Usage: ./dev/v3/sandbox <command>
  up                     Create/start Ubuntu 26.04 (4 CPUs, 4 GiB, 40 GiB)
  ssh                    Interactive root SSH on 127.0.0.1:2222
  exec <program> [args]  Execute a guest command as root
  gordon <program> [...] Execute as the unprivileged gordon user
  sync                   Copy tracked + non-ignored files to /home/gordon/src/gordon
  stop                   Gracefully shut down, preserving disk
  destroy --yes          Delete this VM, its disk, and its private network

No sudo, host DNS changes, host CA installation, or Gordon installation.
See dev/v3/README.md for fixtures, SOCKS, TLS, and L4 test commands.
`

type sandbox struct {
	ctx                    context.Context
	root, state, cache, id string
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(help)
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := os.Getenv("GORDON_REPO_ROOT")
	if root == "" {
		return errors.New("use ./dev/v3/sandbox")
	}
	s := sandbox{ctx: ctx, root: root, state: filepath.Join(env("XDG_STATE_HOME", filepath.Join(home, ".local/state")), "gordon/v3-sandbox"), cache: filepath.Join(env("XDG_CACHE_HOME", filepath.Join(home, ".cache")), "gordon/v3-sandbox")}
	if !filepath.IsAbs(s.state) || !filepath.IsAbs(s.cache) {
		return errors.New("XDG paths must be absolute")
	}
	if err := os.MkdirAll(s.state, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.state+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another sandbox command is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing the descriptor also releases the lock
	return s.dispatch(args)
}

func (s *sandbox) dispatch(args []string) error {
	switch args[0] {
	case "up":
		return s.up()
	case "ssh", "exec", "gordon", "sync", "stop", "destroy":
		if err := s.owned(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	switch args[0] {
	case "ssh":
		return s.cmd("ssh", append([]string{"-t"}, s.sshArgs()...)...)
	case "exec":
		return s.remote(false, args[1:]...)
	case "gordon":
		return s.remote(true, args[1:]...)
	case "sync":
		return s.sync()
	case "stop":
		return s.stop()
	case "destroy":
		if len(args) != 2 || args[1] != "--yes" {
			return errors.New("destroy removes VM data; pass --yes to confirm")
		}
		return s.destroy()
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func quote(v string) string { return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'" }
func (s *sandbox) command(name string, args ...string) *exec.Cmd {
	// #nosec G204 -- fixed host executables; remote commands are explicit developer input.
	c := exec.CommandContext(s.ctx, name, args...)
	c.Env = append(os.Environ(), "LC_ALL=C")
	return c
}
func (s *sandbox) cmd(name string, args ...string) error {
	c := s.command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
func (s *sandbox) out(name string, args ...string) (string, error) {
	b, err := s.command(name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, b)
	}
	return strings.TrimSpace(string(b)), nil
}
func (s *sandbox) virsh(args ...string) error {
	return s.cmd("virsh", append([]string{"-c", uri}, args...)...)
}
func (s *sandbox) inspect(args ...string) (string, error) {
	return s.out("virsh", append([]string{"-c", uri}, args...)...)
}
func (s *sandbox) contains(command, flag string) (bool, error) {
	output, err := s.inspect(command, "--all", flag)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Fields(output) {
		if line == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *sandbox) owned() error {
	data, err := os.ReadFile(filepath.Join(s.state, "uuid"))
	if err != nil {
		return errors.New("no ownership record; refusing to touch existing libvirt resources")
	}
	s.id = strings.TrimSpace(string(data))
	if len(s.id) != 36 {
		return errors.New("invalid ownership UUID")
	}
	for _, r := range []struct{ list, flag, uuid string }{{"list", "--name", "domuuid"}, {"net-list", "--name", "net-uuid"}, {"pool-list", "--name", "pool-uuid"}} {
		exists, err := s.contains(r.list, r.flag)
		if err != nil {
			return err
		}
		if exists {
			id, err := s.inspect(r.uuid, name)
			if err != nil {
				return err
			}
			if id != s.id {
				return fmt.Errorf("refusing foreign resource %s (%s)", name, r.uuid)
			}
		}
	}
	return nil
}

func (s *sandbox) up() error {
	for _, tool := range []string{"virsh", "qemu-img", "cloud-localds", "ssh", "ssh-keygen", "curl", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			return err
		}
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return errors.New("amd64 KVM host required")
	}
	if _, err := s.inspect("uri"); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.state, "uuid")); os.IsNotExist(err) {
		for _, list := range []string{"list", "net-list", "pool-list"} {
			exists, err := s.contains(list, "--name")
			if err != nil {
				return err
			}
			if exists {
				return errors.New("resource name already exists without ownership; refusing adoption")
			}
		}
		s.id = fmt.Sprintf("%x-%x-%x-%x-%x", random(4), random(2), random(2), random(2), random(6))
		if err := os.WriteFile(filepath.Join(s.state, "uuid"), []byte(s.id), 0600); err != nil {
			return err
		}
	}
	if err := s.owned(); err != nil {
		return err
	}
	exists, err := s.contains("list", "--name")
	if err != nil {
		return err
	}
	if !exists {
		if err := s.create(); err != nil {
			return fmt.Errorf("creation incomplete; destroy --yes before retrying: %w", err)
		}
	}
	return s.start()
}
func random(n int) []byte { return []byte(rand.Text())[:n] }

func (s *sandbox) start() error {
	// Restore only our existing resources after an ordinary host reboot.
	for _, r := range []struct{ list, start string }{{"net-list", "net-start"}, {"pool-list", "pool-start"}} {
		active, err := s.inspect(r.list, "--name")
		if err != nil {
			return err
		}
		if !strings.Contains(active, name) {
			if err := s.virsh(r.start, name); err != nil {
				return err
			}
		}
	}
	state, err := s.inspect("domstate", name)
	if err != nil {
		return err
	}
	if state != "running" {
		if err := s.virsh("start", name); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Minute)
	defer cancel()
	ready := *s
	ready.ctx = ctx
	fmt.Println("Waiting for pinned-key SSH and cloud-init (up to 10 minutes)...")
	for {
		if _, err := ready.out("ssh", append(ready.sshArgs(), "true")...); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return ready.remote(false, "cloud-init", "status", "--wait")
}

func (s *sandbox) create() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:2222")
	if err != nil {
		return fmt.Errorf("SSH port already in use: %w", err)
	}
	listener.Close()
	routes, err := s.out("ip", "-4", "route", "show", "table", "all", "match", "198.18.77.0/24")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(routes, "\n") {
		if line != "" && !strings.HasPrefix(line, "default ") {
			return fmt.Errorf("test subnet overlaps an existing route: %s", line)
		}
	}
	image, err := s.image()
	if err != nil {
		return err
	}
	if err := s.prepare(); err != nil {
		return err
	}
	if err := s.virsh("net-define", filepath.Join(s.state, "network.xml")); err != nil {
		return err
	}
	if err := s.virsh("pool-define", filepath.Join(s.state, "pool.xml")); err != nil {
		return err
	}
	if err := s.virsh("pool-build", name); err != nil {
		return err
	}
	if err := s.virsh("pool-start", name); err != nil {
		return err
	}
	if err := s.disk(image); err != nil {
		return err
	}
	seed := filepath.Join(s.state, "seed.iso")
	if err := s.cmd("cloud-localds", "--network-config="+filepath.Join(s.state, "network-config"), seed, filepath.Join(s.state, "user-data"), filepath.Join(s.state, "meta-data")); err != nil {
		return err
	}
	if err := s.upload("seed.iso", seed, "raw", "4M"); err != nil {
		return err
	}
	return s.virsh("define", filepath.Join(s.state, "domain.xml"))
}

func (s *sandbox) prepare() error {
	for _, key := range []string{"client", "host"} {
		if _, err := os.Stat(filepath.Join(s.state, key)); err == nil {
			return errors.New("incomplete creation; destroy --yes before retrying")
		}
		if err := s.cmd("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(s.state, key)); err != nil {
			return err
		}
	}
	pub, err := os.ReadFile(filepath.Join(s.state, "client.pub"))
	if err != nil {
		return err
	}
	host, err := os.ReadFile(filepath.Join(s.state, "host"))
	if err != nil {
		return err
	}
	hostPub, err := os.ReadFile(filepath.Join(s.state, "host.pub"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.state, "known_hosts"), []byte("[127.0.0.1]:2222 "+string(hostPub)), 0600); err != nil {
		return err
	}
	values := map[string]string{"UUID": s.id, "ClientKey": strings.TrimSpace(string(pub)), "HostKey": "    " + strings.ReplaceAll(strings.TrimSpace(string(host)), "\n", "\n    ")}
	for _, file := range []string{"network.xml", "pool.xml", "domain.xml", "user-data", "meta-data", "network-config"} {
		if err := s.render(file, values); err != nil {
			return err
		}
	}
	return nil
}

func (s *sandbox) render(file string, values map[string]string) error {
	data, err := os.ReadFile(filepath.Join(s.root, "dev/v3/cloud-init", file))
	if err != nil {
		return err
	}
	text := string(data)
	for key, value := range values {
		text = strings.ReplaceAll(text, "{{"+key+"}}", value)
	}
	return os.WriteFile(filepath.Join(s.state, file), []byte(text), 0600)
}
func (s *sandbox) image() (string, error) {
	if err := os.MkdirAll(s.cache, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(s.cache, imageSHA+".img")
	if validHash(path, imageSHA) {
		return path, nil
	}
	temp := path + ".part"
	defer os.Remove(temp)
	if err := s.cmd("curl", "--fail", "--location", "--proto", "=https", "--max-time", "900", "--output", temp, imageURL); err != nil {
		return "", err
	}
	if !validHash(temp, imageSHA) {
		return "", errors.New("ubuntu image checksum mismatch")
	}
	return path, os.Rename(temp, path)
}
func validHash(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == want
}
func (s *sandbox) disk(image string) error {
	path := filepath.Join(s.state, "disk.qcow2")
	defer os.Remove(path)
	if err := s.cmd("qemu-img", "convert", "-O", "qcow2", image, path); err != nil {
		return err
	}
	if err := s.cmd("qemu-img", "resize", path, "40G"); err != nil {
		return err
	}
	return s.upload("disk.qcow2", path, "qcow2", "40G")
}
func (s *sandbox) upload(nameInPool, path, format, capacity string) error {
	if err := s.virsh("vol-create-as", name, nameInPool, capacity, "--format", format); err != nil {
		return err
	}
	return s.virsh("vol-upload", nameInPool, path, "--pool", name, "--sparse")
}

func (s *sandbox) sshArgs() []string {
	return []string{"-F", "/dev/null", "-p", "2222", "-i", filepath.Join(s.state, "client"), "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ForwardAgent=no", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + filepath.Join(s.state, "known_hosts"), "-o", "ConnectTimeout=3", "root@127.0.0.1"}
}
func (s *sandbox) remote(gordon bool, args ...string) error {
	if len(args) == 0 {
		return errors.New("remote program required")
	}
	words := make([]string, len(args))
	for i, arg := range args {
		words[i] = quote(arg)
	}
	command := strings.Join(words, " ")
	if gordon {
		command = `uid=$(id -u gordon); cd /home/gordon && exec runuser -u gordon -- env HOME=/home/gordon XDG_RUNTIME_DIR=/run/user/$uid DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus sh -c ` + quote(command)
	}
	sshArgs := s.sshArgs()
	if len(args) == 1 && (args[0] == "bash" || args[0] == "sh") {
		sshArgs = append([]string{"-t"}, sshArgs...)
	}
	return s.cmd("ssh", append(sshArgs, command)...)
}
func (s *sandbox) sync() error {
	// Git determines what belongs in the snapshot; ignored files and .git stay local.
	fileBytes, err := s.command("git", "-C", s.root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return err
	}
	// GNU tar does not dereference symlinks. Extract as gordon, never as root.
	c := s.command("tar", "-C", s.root, "--null", "--verbatim-files-from", "-T", "-", "-czf", "-")
	c.Stdin = strings.NewReader(string(fileBytes))
	c.Stderr = os.Stderr
	r, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := s.remote(false, "install", "-d", "-o", "gordon", "-g", "gordon", "-m", "0700", "/home/gordon/src/gordon"); err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	dest := `uid=$(id -u gordon); cd /home/gordon; exec runuser -u gordon -- tar --no-same-owner --no-same-permissions -xzf - -C /home/gordon/src/gordon`
	receiver := s.command("ssh", append(s.sshArgs(), dest)...)
	receiver.Stdin = r
	receiver.Stdout = os.Stdout
	receiver.Stderr = os.Stderr
	err = receiver.Run()
	r.Close()
	producerErr := c.Wait()
	if err != nil {
		return err
	}
	return producerErr
}

func (s *sandbox) stop() error {
	state, err := s.inspect("domstate", name)
	if err != nil {
		return err
	}
	if state == "shut off" {
		return nil
	}
	if err := s.virsh("shutdown", name); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	for {
		state, err = s.inspect("domstate", name)
		if err != nil {
			return err
		}
		if state == "shut off" {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("shutdown still pending; disk preserved")
		case <-time.After(time.Second):
		}
	}
}
func (s *sandbox) destroy() error {
	exists, err := s.contains("list", "--name")
	if err != nil {
		return err
	}
	if exists {
		state, err := s.inspect("domstate", name)
		if err != nil {
			return err
		}
		if state != "shut off" {
			if err := s.virsh("destroy", name); err != nil {
				return err
			}
		}
		if err := s.virsh("undefine", name, "--nvram"); err != nil {
			return err
		}
	}
	if err := s.destroyPool(); err != nil {
		return err
	}
	exists, err = s.contains("net-list", "--name")
	if err != nil {
		return err
	}
	if exists {
		active, err := s.inspect("net-list", "--name")
		if err != nil {
			return err
		}
		if strings.Contains(active, name) {
			if err := s.virsh("net-destroy", name); err != nil {
				return err
			}
		}
		if err := s.virsh("net-undefine", name); err != nil {
			return err
		}
	}
	return os.RemoveAll(s.state)
}
func (s *sandbox) destroyPool() error {
	exists, err := s.contains("pool-list", "--name")
	if err != nil || !exists {
		return err
	}
	active, err := s.inspect("pool-list", "--name")
	if err != nil {
		return err
	}
	if !strings.Contains(active, name) {
		if err := s.virsh("pool-start", name); err != nil {
			return err
		}
	}
	output, err := s.inspect("vol-list", name)
	if err != nil {
		return err
	}
	volumes, err := volumeNames(output)
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if err := s.virsh("vol-delete", v, "--pool", name); err != nil {
			return err
		}
	}
	if err := s.virsh("pool-destroy", name); err != nil {
		return err
	}
	if err := s.virsh("pool-delete", name); err != nil {
		return err
	}
	return s.virsh("pool-undefine", name)
}

func volumeNames(output string) ([]string, error) {
	lines := strings.Split(output, "\n")
	if len(lines) < 2 || strings.Join(strings.Fields(lines[0]), " ") != "Name Path" {
		return nil, errors.New("unrecognized volume table; refusing deletion")
	}
	var names []string
	for _, line := range lines[2:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 || (fields[0] != "disk.qcow2" && fields[0] != "seed.iso") || fields[1] != "/var/lib/libvirt/images/"+name+"/"+fields[0] {
			return nil, fmt.Errorf("unrecognized volume %q; refusing deletion", line)
		}
		names = append(names, fields[0])
	}
	return names, nil
}
