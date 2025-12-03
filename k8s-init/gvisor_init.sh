set -e
ARCH=$(uname -m)
URL=https://storage.googleapis.com/gvisor/releases/release/latest/${ARCH}

wget ${URL}/runsc -O runsc

wget ${URL}/containerd-shim-runsc-v1 -O containerd-shim-runsc-v1

chmod +x runsc containerd-shim-runsc-v1

sudo mv runsc containerd-shim-runsc-v1 /usr/local/bin

sudo /usr/local/bin/runsc install

sudo systemctl restart containerd


# sudo vi /etc/containerd/config.toml

# 아래 containerd.runtimes.runsc 추가
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runc]
  runtime_type = 'io.containerd.runc.v2'
  runtime_path = ''
  pod_annotations = []
  container_annotations = []
  privileged_without_host_devices = false
  privileged_without_host_devices_all_devices_allowed = false
  cgroup_writable = false
  base_runtime_spec = ''
  cni_conf_dir = ''
  cni_max_conf_num = 0
  snapshotter = ''
  sandboxer = 'podsandbox'
  io_type = ''
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runsc]
  runtime_type = 'io.containerd.runsc.v1'
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runc.options]
  BinaryName = ''