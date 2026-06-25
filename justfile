# justfile for mysql-mcp
# Run `just` to see all available commands.

set shell := ["bash", "-euo", "pipefail", "-c"]

# Default — list recipes.
default:
    @just --list --unsorted

# ─────────────────────────── Build & Test ───────────────────────────

# Version baked into the binary at link time.
GO_LDFLAGS := "-X github.com/stubbedev/mysql-mcp/internal/cli.Version=$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

# Build the binary into ./bin/.
build:
    mkdir -p bin
    go build -ldflags="{{GO_LDFLAGS}}" -o bin/mysql-mcp ./cmd/mysql-mcp
    @echo "Built ./bin/mysql-mcp"

# Install into $GOBIN (or $GOPATH/bin).
install:
    go install -ldflags="{{GO_LDFLAGS}}" ./cmd/mysql-mcp
    @echo "Installed mysql-mcp to $(go env GOBIN || echo $(go env GOPATH)/bin)"

# Auto-fix formatting drift.
fmt:
    gofmt -w .

# One-shot per clone; idempotent. CI still runs the same check as the
# authoritative gate — the hook just catches drift earlier.
# Enable the pre-commit gofmt + vet gate (git core.hooksPath = .githooks).
install-hooks:
    #!/usr/bin/env bash
    set -euo pipefail
    git config core.hooksPath .githooks
    echo "git config core.hooksPath = .githooks"
    echo "pre-commit gofmt + vet gate is now active (bypass with --no-verify)."

# Same dev contract as the sync-* recipes: what can be regenerated is.
# Format, then go vet + the full golangci-lint gate.
lint: fmt
    go vet ./...
    golangci-lint run ./...

# Same logic CI runs, exposed for local pre-push verification.
# Strict read-only gate: fail if gofmt would change anything or a linter fires.
lint-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted="$(gofmt -l .)"
    if [ -n "$unformatted" ]; then
        echo "code is not gofmt-clean; run 'just fmt':"
        printf '%s\n' "$unformatted"
        exit 1
    fi
    go vet ./...
    golangci-lint run ./...

test:
    go test ./...

# Tests with the race detector and a coverage profile.
test-race:
    go test -race -coverprofile=coverage.txt ./...

# Brings the container up + tears it down; needs docker and a free port.
# Exercise the tools against a throwaway docker MySQL on port 13306.
test-e2e:
    #!/usr/bin/env bash
    set -euo pipefail
    name=mysqlmcp-e2e
    trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT
    docker rm -f "$name" >/dev/null 2>&1 || true
    docker run -d --name "$name" \
        -e MYSQL_ROOT_PASSWORD=testpw -e MYSQL_DATABASE=demo \
        -p 127.0.0.1:13306:3306 mysql:latest >/dev/null
    echo "waiting for mysql…"
    ready=0
    for i in $(seq 1 45); do
        # An authenticated query is the real readiness signal — mysqladmin ping
        # reports "alive" during init before the root password is applied.
        if docker exec "$name" mysql -uroot -ptestpw -e "SELECT 1" >/dev/null 2>&1; then
            ready=1; break
        fi
        sleep 2
    done
    [ "$ready" = "1" ] || { echo "mysql did not become ready"; exit 1; }
    docker exec "$name" mysql -uroot -ptestpw demo \
        -e "CREATE TABLE IF NOT EXISTS widgets(id INT PRIMARY KEY, name VARCHAR(32));"
    cfg=$(mktemp)
    cat > "$cfg" <<JSON
    {"sources":{"demo":{"engine":"mysql","host":"127.0.0.1","port":13306,"user":"root","password":"testpw","database":"demo","readonly":true}}}
    JSON
    go build -o bin/mysql-mcp ./cmd/mysql-mcp
    { printf '%s\n' \
        '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}' \
        '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
        '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_tables","arguments":{"source":"demo"}}}'; sleep 1; } \
    | bin/mysql-mcp serve --config "$cfg" 2>/dev/null | grep -q '"row_count"' \
        && echo "e2e OK" || { echo "e2e FAILED"; exit 1; }

# ─────────────────────────── Generated artifacts ───────────────────────────

# Run everything CI runs as the merge gate.
check: lint test sync-schema sync-docs sync-flake

# Cheap (pure reflection) so it runs on every `just check`; CI asserts no
# drift on PRs and auto-commits on master.
# Regenerate schema/config.schema.json from the config.Config Go types.
sync-schema:
    #!/usr/bin/env bash
    set -euo pipefail
    go run ./cmd/mysql-mcp gen-schema --output schema/config.schema.json
    if [ -n "$(git status --porcelain schema/config.schema.json)" ]; then
        echo "sync-schema: regenerated schema/config.schema.json"
    else
        echo "sync-schema: schema already in sync"
    fi

# Catches config/API drift in PRs by comparing the rewrite against git.
# Regenerate docs/configuration.md (config reference) + docs/api.md (Go API).
sync-docs:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p docs
    go run ./cmd/mysql-mcp gen-config-docs --output docs/configuration.md
    if ! command -v gomarkdoc >/dev/null 2>&1; then
        go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@latest
        export PATH="$(go env GOPATH)/bin:$PATH"
    fi
    gomarkdoc --output docs/api.md ./...
    if [ -n "$(git status --porcelain docs/configuration.md docs/api.md)" ]; then
        echo "sync-docs: regenerated generated docs"
    else
        echo "sync-docs: generated docs already in sync"
    fi

# Keep flake.nix's vendorHash aligned with the current go.sum.
#
# A sha256 of go.sum is embedded as a `# go-sum:` line in flake.nix. When
# the cached digest matches go.sum on disk, sync-flake returns immediately
# without running `nix build`. That makes it cheap enough to run on every
# `just check`, so a dev `go get` flow can never push a master commit that
# breaks nix CI. Pass `--force` to re-run the nix build regardless.
# Sync flake.nix vendorHash to go.sum (cached; skips nix build when unchanged).
sync-flake force="":
    #!/usr/bin/env bash
    set -euo pipefail
    FORCE=0
    [ "{{force}}" = "--force" ] && FORCE=1

    GO_SUM_HASH=$(sha256sum go.sum | awk '{print $1}')
    CACHED_HASH=$(awk -F': ' '/^[[:space:]]*#[[:space:]]*go-sum:/ {print $2; exit}' flake.nix | tr -d ' ')

    if [ "$FORCE" = "0" ] && [ "$GO_SUM_HASH" = "$CACHED_HASH" ]; then
        echo "sync-flake: up-to-date (go.sum=$GO_SUM_HASH)"
        exit 0
    fi

    echo "sync-flake: refreshing vendorHash (go.sum changed)"
    SENTINEL="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
    sed -i -E 's|^(\s*vendorHash = )"sha256-[^"]*";|\1"'"$SENTINEL"'";|' flake.nix

    set +e
    OUT=$(nix build .#default --no-link 2>&1)
    BUILD_STATUS=$?
    set -e
    NEW_HASH=$(printf '%s\n' "$OUT" | awk '/got:[[:space:]]*sha256-/ {print $NF; exit}')
    if [ -z "$NEW_HASH" ]; then
        if [ "$BUILD_STATUS" = "0" ]; then
            echo "sync-flake: unexpected nix build success with sentinel hash" >&2
        fi
        printf '%s\n' "$OUT" >&2
        echo "sync-flake: nix build did not print 'got: sha256-…'" >&2
        exit 1
    fi
    sed -i -E 's|^(\s*vendorHash = )"sha256-[^"]*";|\1"'"$NEW_HASH"'";|' flake.nix
    if grep -q '^[[:space:]]*# go-sum:' flake.nix; then
        sed -i -E 's|^(\s*# go-sum:).*|\1 '"$GO_SUM_HASH"'|' flake.nix
    else
        sed -i -E 's|^(\s*vendorHash = )|          # go-sum: '"$GO_SUM_HASH"'\n\1|' flake.nix
    fi
    echo "sync-flake: vendorHash=$NEW_HASH go-sum=$GO_SUM_HASH"

    # Hard guard: never leave the sentinel behind — CI would fail on mismatch.
    if grep -q "$SENTINEL" flake.nix; then
        echo "sync-flake: refusing to leave sentinel vendorHash in flake.nix" >&2
        exit 1
    fi
    nix build .#default --no-link

clean:
    rm -rf bin/ coverage.txt

# ─────────────────────────── Nix ───────────────────────────

nix-build:
    nix build .#default

nix-check:
    nix flake check --print-build-logs

# ─────────────────────────── Release ───────────────────────────

release-preview:
    #!/usr/bin/env bash
    set -euo pipefail
    CURRENT_TAG=$(git tag -l 'v*.*.*' --sort=-v:refname | head -1)
    CURRENT_TAG=${CURRENT_TAG:-v0.0.0}
    CURRENT_VERSION=${CURRENT_TAG#v}
    MAJOR=$(echo "$CURRENT_VERSION" | cut -d. -f1)
    MINOR=$(echo "$CURRENT_VERSION" | cut -d. -f2)
    PATCH=$(echo "$CURRENT_VERSION" | cut -d. -f3)
    echo "Current tag: $CURRENT_TAG"
    echo "  release-major: v$((MAJOR + 1)).0.0"
    echo "  release-minor: v${MAJOR}.$((MINOR + 1)).0"
    echo "  release-patch: v${MAJOR}.${MINOR}.$((PATCH + 1))"

_release-checks:
    #!/usr/bin/env bash
    set -euo pipefail
    BRANCH=$(git rev-parse --abbrev-ref HEAD)
    DEFAULT_BRANCH=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null | sed 's|^origin/||' || true)
    DEFAULT_BRANCH=${DEFAULT_BRANCH:-master}
    if [ "$BRANCH" != "$DEFAULT_BRANCH" ]; then
        echo "Error: not on default branch '$DEFAULT_BRANCH' (currently '$BRANCH')." >&2
        exit 1
    fi
    just check
    if [ -n "$(git status --porcelain)" ]; then
        echo "check produced changes — staging + committing."
        git add -A
        git commit -m "chore: regenerate artifacts for release"
    fi

_release bump:
    #!/usr/bin/env bash
    set -euo pipefail
    just _release-checks
    CURRENT_TAG=$(git tag -l 'v*.*.*' --sort=-v:refname | head -1)
    CURRENT_TAG=${CURRENT_TAG:-v0.0.0}
    CURRENT_VERSION=${CURRENT_TAG#v}
    MAJOR=$(echo "$CURRENT_VERSION" | cut -d. -f1)
    MINOR=$(echo "$CURRENT_VERSION" | cut -d. -f2)
    PATCH=$(echo "$CURRENT_VERSION" | cut -d. -f3)
    case "{{bump}}" in
        major) NEW="$((MAJOR + 1)).0.0" ;;
        minor) NEW="${MAJOR}.$((MINOR + 1)).0" ;;
        patch) NEW="${MAJOR}.${MINOR}.$((PATCH + 1))" ;;
        *) echo "unknown bump kind: {{bump}}"; exit 1 ;;
    esac
    git tag -a "v${NEW}" -m "v${NEW}"
    git push origin HEAD
    git push origin "v${NEW}"
    echo "Tagged v${NEW}. The flake derives this version from the git tag at build time."

release-patch: (_release "patch")
release-minor: (_release "minor")
release-major: (_release "major")
