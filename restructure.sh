#!/usr/bin/env bash
# One-off: rename baseline/ to p2p/ and move the container folders into
# testbed/. Run from the repository root with a clean working tree.
set -euo pipefail

[[ -f docker-compose.yml && -d baseline ]] || { echo "run from the repo root (before the move)"; exit 1; }
[[ -z "$(git status --porcelain --untracked-files=no)" ]] || { echo "commit or stash your changes first"; exit 1; }

# 1. Move the folders (git mv keeps each file's history).
git mv baseline p2p
mkdir -p testbed
git mv router testbed/router
git mv host testbed/host
git mv stun testbed/stun

# 2. Go module path: p2ptestbed/baseline -> p2ptestbed/p2p
perl -pi -e 's{^module p2ptestbed/baseline$}{module p2ptestbed/p2p}' p2p/go.mod
grep -rl 'p2ptestbed/baseline' p2p --include='*.go' | xargs perl -pi -e 's{"p2ptestbed/baseline/}{"p2ptestbed/p2p/}g'

# 3. Build paths.
perl -pi -e 's{build: \./router}{build: ./testbed/router};
             s{build: \./stun}{build: ./testbed/stun};
             s{dockerfile: host/Dockerfile}{dockerfile: testbed/host/Dockerfile}' docker-compose.yml
perl -pi -e 's{^COPY baseline/ \./}{COPY p2p/ ./};
             s{^COPY host/configure-host\.sh}{COPY testbed/host/configure-host.sh}' testbed/host/Dockerfile

# 4. Paths in docs and comments. The lookbehind skips names that merely end
#    in "baseline/" (results/path_baseline/...).
perl -pi -e 's{(?<![\w/])baseline/}{p2p/}g;
             s{(?<![\w/])(router|host|stun)/}{testbed/$1/}g' \
  README.md docker-compose.yml .dockerignore experiments/*.toml testbed/*/Dockerfile

# 5. The Python tests (tests/) are not needed inside the host image.
grep -qx 'tests' .dockerignore || printf 'tests\n' >> .dockerignore

git add p2p testbed docker-compose.yml README.md .dockerignore experiments
echo "Done. Check with: git status && docker compose build"

