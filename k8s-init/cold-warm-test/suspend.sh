#!/bin/bash

CONTAINER_ID=$(sudo crictl ps --label io.kubernetes.pod.name=hello-gvisor -q | head -n 1)

if [ -z "$CONTAINER_ID" ]; then
  echo "Error: 컨테이너를 찾을 수 없습니다."
  echo "Debug info: 실행 중인 모든 컨테이너 목록:"
  sudo crictl ps
  exit 1
fi

echo "Target Container ID: $CONTAINER_ID"
echo "----------------------------------------"

RUNSC_BIN="/usr/local/bin/runsc"
RUNSC_ROOT="/run/containerd/runsc/k8s.io"

echo "[Measuring SUSPEND (Pause) Time]"
time sudo $RUNSC_BIN --root $RUNSC_ROOT pause $CONTAINER_ID

echo "----------------------------------------"
echo "Paused. Waiting 2 seconds..."
sleep 2

echo "[Measuring RESUME Time]"
time sudo $RUNSC_BIN --root $RUNSC_ROOT resume $CONTAINER_ID

echo "----------------------------------------"
echo "Done."