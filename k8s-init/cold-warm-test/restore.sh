#!/bin/bash

POD_NAME_KEYWORD="hello-gvisor"
CHECKPOINT_DIR="/tmp/gvisor-checkpoint"
BUNDLE_BACKUP_DIR="/tmp/gvisor-bundle-backup"
RUNSC_BIN="/usr/local/bin/runsc"
RUNSC_ROOT="/run/containerd/runsc/k8s.io" 

if [ -d "$CHECKPOINT_DIR" ]; then sudo rm -rf "$CHECKPOINT_DIR"; fi
if [ -d "$BUNDLE_BACKUP_DIR" ]; then sudo rm -rf "$BUNDLE_BACKUP_DIR"; fi

mkdir -p "$CHECKPOINT_DIR"
mkdir -p "$BUNDLE_BACKUP_DIR"

CONTAINER_ID=$(sudo crictl ps --label io.kubernetes.pod.name=$POD_NAME_KEYWORD -q | head -n 1)

if [ -z "$CONTAINER_ID" ]; then
  echo "Error: 컨테이너를 찾을 수 없습니다. 파드가 Running 상태인지 확인하세요."
  exit 1
fi

echo "Target Container ID: $CONTAINER_ID"

echo "Searching for config.json..."
REAL_CONFIG_PATH=$(sudo find /run/containerd -name config.json 2>/dev/null | grep "$CONTAINER_ID" | head -n 1)

if [ -z "$REAL_CONFIG_PATH" ]; then
  echo "Error: 설정 파일(config.json)을 자동으로 찾을 수 없습니다."
  exit 1
fi

REAL_BUNDLE_DIR=$(dirname "$REAL_CONFIG_PATH")

echo "Found Config at:     $REAL_CONFIG_PATH"
echo "Original Bundle Dir: $REAL_BUNDLE_DIR"
echo "----------------------------------------"

echo "[Measuring CHECKPOINT Time]"
time sudo $RUNSC_BIN --root $RUNSC_ROOT checkpoint \
    --image-path "$CHECKPOINT_DIR" \
    --leave-running \
    $CONTAINER_ID

echo "----------------------------------------"
echo "Checkpoint created."

echo "Backing up bundle environment..."

sudo cp "$REAL_CONFIG_PATH" "$BUNDLE_BACKUP_DIR/config.json"

if [ -d "$REAL_BUNDLE_DIR/rootfs" ]; then
    echo "Creating symlink for rootfs..."
    sudo ln -sf "$REAL_BUNDLE_DIR/rootfs" "$BUNDLE_BACKUP_DIR/rootfs"
else
    echo "Original rootfs not found. Creating empty mountpoint directory..."
    sudo mkdir -p "$BUNDLE_BACKUP_DIR/rootfs"
fi

echo "----------------------------------------"
echo "[Preparing for Restore]"
echo "Stopping container..."
sudo crictl stop $CONTAINER_ID > /dev/null

# Restore 
echo "[Measuring RESTORE Time]"

time sudo $RUNSC_BIN --root $RUNSC_ROOT restore \
    --image-path "$CHECKPOINT_DIR" \
    --bundle "$BUNDLE_BACKUP_DIR" \
    --detach \
    $CONTAINER_ID

echo "----------------------------------------"
echo "Measurement Complete."