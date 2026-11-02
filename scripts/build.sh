#!/bin/bash
# Construit l'image Docker (nginx + public/ + config) et la pousse sur Docker Hub.
# Version = tag git du commit courant, sinon "latest". La clé SNCF n'est jamais incluse (voir .dockerignore).
# Usage : DOCKER_USERNAME=… DOCKER_PASSWORD=… scripts/build.sh
#         NO_PUSH=1 scripts/build.sh   → construit et vérifie seulement (CI des pull requests)
set -euo pipefail
cd "$(dirname "$0")/.."

IMAGE_NAME="${IMAGE_NAME:-melvinvmegen/nuisance-sonore-train}"
VERSION=$(git tag --points-at HEAD | head -n1)
if [ -z "$VERSION" ]; then
    VERSION="latest"
fi

echo "Building docker image $IMAGE_NAME:$VERSION"
if ! type docker > /dev/null;
then
    echo "Docker not found. Please install docker to build docker image"
    exit 1
fi

if [ -n "$(git status --porcelain -- public deploy Dockerfile .dockerignore)" ]; then
    echo "Warning: uncommitted changes in public/, deploy/ or Dockerfile will be included in the image"
fi

echo "Building docker image ..."
docker build -t "$IMAGE_NAME:$VERSION" .

# Vérifie la config nginx de l'image (sans clé SNCF : le include secrets/sncf_auth*.inc ne trouve rien, c'est normal)
echo "Checking nginx config ..."
docker run --rm "$IMAGE_NAME:$VERSION" nginx -t

if [ "${NO_PUSH:-}" = "1" ]; then
    echo "NO_PUSH=1: image built and checked, not pushed"
    exit 0
fi

if [[ -z "${DOCKER_USERNAME:-}" ]] || [[ -z "${DOCKER_PASSWORD:-}" ]];
then
    echo "Docker idents not found. Please DOCKER_USERNAME and DOCKER_PASSWORD environment variables are needed to be able to push"
    exit 1
fi

echo "Pushing docker image ..."
printf '%s' "$DOCKER_PASSWORD" | docker login -u "$DOCKER_USERNAME" --password-stdin
docker push "$IMAGE_NAME:$VERSION"
if [ "$VERSION" != "latest" ]; then
    docker tag "$IMAGE_NAME:$VERSION" "$IMAGE_NAME:latest"
    docker push "$IMAGE_NAME:latest"
fi
