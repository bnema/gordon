#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
PASS=0
FAIL=0
SHA=0123456789abcdef0123456789abcdef01234567

fail() {
    echo "not ok - $1"
    FAIL=$((FAIL + 1))
}

pass() {
    echo "ok - $1"
    PASS=$((PASS + 1))
}

run_case() {
    shift
    case_dir=$(mktemp -d)
    mkdir -p "$case_dir/bin" "$case_dir/home" "$case_dir/install"

    cat >"$case_dir/bin/uname" <<'EOF'
#!/bin/sh
[ "$1" = "-s" ] && echo Linux || echo x86_64
EOF
    cat >"$case_dir/bin/id" <<'EOF'
#!/bin/sh
[ "$1" = "-u" ] && { echo 1000; exit 0; }
exec /usr/bin/id "$@"
EOF
    cat >"$case_dir/bin/curl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>'$case_dir/curl.log'
case "\$*" in
  *api.github.com/repos/bnema/gordon/commits/next*) printf '%s\n' '{"sha":"$SHA"}' ;;
  *codeload.github.com/bnema/gordon/tar.gz/$SHA*) : >'$case_dir/source.tar.gz' ;;
  *) exit 90 ;;
esac
EOF
    cat >"$case_dir/bin/tar" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>'$case_dir/tar.log'
case "\$1" in
  -tzf) printf '%s\n' 'gordon-$SHA/' 'gordon-$SHA/go.mod' 'gordon-$SHA/main.go' ;;
  -tvzf) printf '%s\n' 'drwxr-xr-x root/root 0 date gordon-$SHA/' '-rw-r--r-- root/root 0 date gordon-$SHA/go.mod' '-rw-r--r-- root/root 0 date gordon-$SHA/main.go' ;;
  -xzf)
    while [ "\$#" -gt 0 ]; do
      if [ "\$1" = -C ]; then
        shift
        mkdir -p "\$1/gordon-$SHA"
        printf 'module example\n\ngo 1.27\n' >"\$1/gordon-$SHA/go.mod"
        : >"\$1/gordon-$SHA/main.go"
        exit 0
      fi
      shift
    done
    ;;
esac
EOF
    cat >"$case_dir/bin/go" <<EOF
#!/bin/sh
printf '%s\n' "GOOS=\${GOOS-} GOARCH=\${GOARCH-} GOTOOLCHAIN=\${GOTOOLCHAIN-} \$*" >>'$case_dir/go.log'
if [ "\$1 \$2" = 'env GOVERSION' ]; then echo go1.27.1; exit 0; fi
if [ "\$1" = build ]; then
  while [ "\$#" -gt 0 ]; do [ "\$1" = -o ] && { shift; : >"\$1"; chmod 755 "\$1"; exit 0; }; shift; done
fi
exit 91
EOF
    cat >"$case_dir/bin/install" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>'$case_dir/install.log'
cp "\$3" "\$4"
EOF
    cat >"$case_dir/bin/mv" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >>'$case_dir/mv.log'
[ "\$1" = -f ] && shift
/bin/mv "\$1" "\$2"
EOF
    chmod +x "$case_dir/bin/"*
    mkdir -p "$case_dir/tmp"

    if (PATH="$case_dir/bin:/usr/bin:/bin" HOME="$case_dir/home" SHELL=/bin/bash TMPDIR="$case_dir/tmp" GORDON_UPDATE_PATH=0 "$@") >"$case_dir/out" 2>&1; then
        status=0
    else
        status=$?
    fi
}

test_next_build() {
    run_case next-build env GORDON_CHANNEL=next sh "$ROOT/install.sh"
    [ "$status" -eq 0 ] || { cat "$case_dir/out"; fail "next channel builds pinned source"; return; }
    if grep -q "commits/next" "$case_dir/curl.log" &&
        grep -q "codeload.github.com/bnema/gordon/tar.gz/$SHA" "$case_dir/curl.log" &&
        grep -q -- "-X main.version=next-$SHA" "$case_dir/go.log" &&
        grep -q "GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local" "$case_dir/go.log" &&
        grep -q "UNVERIFIED DEVELOPMENT BUILD" "$case_dir/out" &&
        [ -x "$case_dir/home/.local/bin/gordon" ]; then
        pass "next channel builds pinned source"
    else
        fail "next channel builds pinned source"
    fi
}

test_conflicts() {
    run_case version-conflict env GORDON_CHANNEL=next GORDON_VERSION=v1.2.3 sh "$ROOT/install.sh"
    if [ "$status" -ne 0 ] && grep -q "GORDON_VERSION cannot be used" "$case_dir/out"; then
        pass "next rejects GORDON_VERSION"
    else
        fail "next rejects GORDON_VERSION"
    fi

    run_case prerelease-conflict env GORDON_CHANNEL=next GORDON_PRERELEASE=1 sh "$ROOT/install.sh"
    if [ "$status" -ne 0 ] && grep -q "GORDON_PRERELEASE cannot be used" "$case_dir/out"; then
        pass "next rejects GORDON_PRERELEASE"
    else
        fail "next rejects GORDON_PRERELEASE"
    fi
}

test_go_version() {
    run_case old-go env GORDON_CHANNEL=next MOCK_GO_VERSION=unused sh "$ROOT/install.sh"
    # Replace the mock after setup, then run once more in the same fixture.
    cat >"$case_dir/bin/go" <<'EOF'
#!/bin/sh
[ "$1 $2" = 'env GOVERSION' ] && { echo go1.26.9; exit 0; }
exit 91
EOF
    chmod +x "$case_dir/bin/go"
    if (PATH="$case_dir/bin:/usr/bin:/bin" HOME="$case_dir/home" SHELL=/bin/bash TMPDIR="$case_dir/tmp" GORDON_UPDATE_PATH=0 GORDON_CHANNEL=next sh "$ROOT/install.sh") >"$case_dir/out" 2>&1; then status=0; else status=$?; fi
    if [ "$status" -ne 0 ] && grep -q "requires Go 1.27 or newer" "$case_dir/out"; then
        pass "next enforces go.mod Go version"
    else
        fail "next enforces go.mod Go version"
    fi
}

test_default_and_path_modes() {
    run_case default-dir env GORDON_CHANNEL=next sh "$ROOT/install.sh"
    if [ "$status" -eq 0 ] && [ -x "$case_dir/home/.local/bin/gordon" ] &&
        grep -q "export PATH='${case_dir}/home/.local/bin':\$PATH" "$case_dir/out"; then
        pass "default install is user-local and prints current-shell instruction"
    else
        fail "default install is user-local and prints current-shell instruction"
    fi

    run_case path-present env GORDON_CHANNEL=next sh "$ROOT/install.sh"
    if (PATH="$case_dir/bin:$case_dir/home/.local/bin:/usr/bin:/bin" HOME="$case_dir/home" SHELL=/bin/bash TMPDIR="$case_dir/tmp" GORDON_UPDATE_PATH=0 GORDON_CHANNEL=next sh "$ROOT/install.sh") >"$case_dir/out" 2>&1; then status=0; else status=$?; fi
    if [ "$status" -eq 0 ] && ! grep -q "Add Gordon to the current shell" "$case_dir/out"; then
        pass "existing PATH needs no update"
    else
        fail "existing PATH needs no update"
    fi

    run_case forced-no env GORDON_CHANNEL=next GORDON_UPDATE_PATH=0 sh "$ROOT/install.sh"
    if [ "$status" -eq 0 ] && [ ! -e "$case_dir/home/.bashrc" ]; then
        pass "forced no does not modify shell config"
    else
        fail "forced no does not modify shell config"
    fi

    run_case invalid-update env GORDON_CHANNEL=next GORDON_UPDATE_PATH=maybe sh "$ROOT/install.sh"
    if [ "$status" -ne 0 ] && grep -q "must be 0 or 1" "$case_dir/out"; then
        pass "invalid PATH mode is rejected"
    else
        fail "invalid PATH mode is rejected"
    fi

    run_case unsafe-dir env GORDON_CHANNEL=next GORDON_INSTALL_DIR="/tmp/unsafe
path" sh "$ROOT/install.sh"
    if [ "$status" -ne 0 ] && grep -q "unsafe control character" "$case_dir/out"; then
        pass "control characters in install directory are rejected"
    else
        fail "control characters in install directory are rejected"
    fi

    run_case trailing-newline env GORDON_CHANNEL=next GORDON_INSTALL_DIR="/tmp/unsafe
" sh "$ROOT/install.sh"
    if [ "$status" -ne 0 ] && grep -q "unsafe control character" "$case_dir/out"; then
        pass "trailing newline in install directory is rejected"
    else
        fail "trailing newline in install directory is rejected"
    fi
}

test_shell_updates() {
    for shell in bash zsh fish; do
        run_case "shell-$shell" env GORDON_CHANNEL=next GORDON_UPDATE_PATH=1 SHELL="/usr/bin/$shell" sh "$ROOT/install.sh"
        case "$shell" in
            bash) config=$case_dir/home/.bashrc; expected="export PATH='${case_dir}/home/.local/bin':\$PATH" ;;
            zsh) config=$case_dir/home/.zshrc; expected="export PATH='${case_dir}/home/.local/bin':\$PATH" ;;
            fish) config=$case_dir/home/.config/fish/config.fish; expected="fish_add_path '${case_dir}/home/.local/bin'" ;;
        esac
        if [ "$status" -eq 0 ] && grep -Fq "$expected" "$config"; then
            pass "$shell PATH update"
        else
            fail "$shell PATH update"
        fi
    done

    run_case idempotent env GORDON_CHANNEL=next GORDON_UPDATE_PATH=1 SHELL=/bin/bash sh "$ROOT/install.sh"
    first_case=$case_dir
    if (PATH="$case_dir/bin:/usr/bin:/bin" HOME="$case_dir/home" SHELL=/bin/bash TMPDIR="$case_dir/tmp" GORDON_UPDATE_PATH=1 GORDON_CHANNEL=next sh "$ROOT/install.sh") >>"$case_dir/out" 2>&1 &&
        [ "$(grep -c '^# >>> Gordon installer PATH >>>$' "$case_dir/home/.bashrc")" -eq 1 ]; then
        pass "PATH marker block is idempotent"
    else
        cat "$first_case/out"
        fail "PATH marker block is idempotent"
    fi

    run_case unsupported env GORDON_CHANNEL=next GORDON_UPDATE_PATH=1 SHELL=/bin/ksh sh "$ROOT/install.sh"
    if [ "$status" -eq 0 ] && grep -q "unsupported or missing SHELL" "$case_dir/out"; then
        pass "unsupported shell gets manual instructions"
    else
        fail "unsupported shell gets manual instructions"
    fi
}

test_explicit_global_override() {
    run_case home-bin env GORDON_CHANNEL=next sh "$ROOT/install.sh"
    if (PATH="$case_dir/bin:/usr/bin:/bin" HOME="$case_dir/home" SHELL=/bin/bash TMPDIR="$case_dir/tmp" GORDON_UPDATE_PATH=1 GORDON_INSTALL_DIR="$case_dir/home/bin" GORDON_CHANNEL=next sh "$ROOT/install.sh") >"$case_dir/out" 2>&1; then status=0; else status=$?; fi
    if [ "$status" -eq 0 ] && [ -x "$case_dir/home/bin/gordon" ] &&
        grep -Fq "export PATH='${case_dir}/home/bin':\$PATH" "$case_dir/home/.bashrc"; then
        pass "HOME bin override is installed and added to PATH"
    else
        fail "HOME bin override is installed and added to PATH"
    fi

    run_case global env GORDON_CHANNEL=next sh "$ROOT/install.sh"
    if (PATH="$case_dir/bin:/usr/bin:/bin" HOME="$case_dir/home" SHELL=/bin/bash TMPDIR="$case_dir/tmp" GORDON_UPDATE_PATH=0 GORDON_INSTALL_DIR="$case_dir/install" GORDON_CHANNEL=next sh "$ROOT/install.sh") >"$case_dir/out" 2>&1; then status=0; else status=$?; fi
    if [ "$status" -eq 0 ] && [ -x "$case_dir/install/gordon" ]; then
        pass "explicit global install override is preserved"
    else
        fail "explicit global install override is preserved"
    fi

    run_case sudo-default env SUDO_USER=test-user GORDON_CHANNEL=next sh "$ROOT/install.sh"
    if [ "$status" -ne 0 ] && grep -q "Refusing a default user-local install" "$case_dir/out" && [ ! -e "$case_dir/home/.local/bin/gordon" ]; then
        pass "sudo default install is refused"
    else
        fail "sudo default install is refused"
    fi
}

test_next_build
test_conflicts
test_go_version
test_default_and_path_modes
test_shell_updates
test_explicit_global_override
printf '%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
