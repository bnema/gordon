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
    mkdir -p "$case_dir/bin" "$case_dir/install"

    cat >"$case_dir/bin/uname" <<'EOF'
#!/bin/sh
[ "$1" = "-s" ] && echo Linux || echo x86_64
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

    if (PATH="$case_dir/bin:/usr/bin:/bin" TMPDIR="$case_dir/tmp" GORDON_INSTALL_DIR="$case_dir/install" "$@") >"$case_dir/out" 2>&1; then
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
        [ -x "$case_dir/install/gordon" ]; then
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
    if (PATH="$case_dir/bin:/usr/bin:/bin" TMPDIR="$case_dir/tmp" GORDON_INSTALL_DIR="$case_dir/install" GORDON_CHANNEL=next sh "$ROOT/install.sh") >"$case_dir/out" 2>&1; then status=0; else status=$?; fi
    if [ "$status" -ne 0 ] && grep -q "requires Go 1.27 or newer" "$case_dir/out"; then
        pass "next enforces go.mod Go version"
    else
        fail "next enforces go.mod Go version"
    fi
}

test_next_build
test_conflicts
test_go_version
printf '%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
