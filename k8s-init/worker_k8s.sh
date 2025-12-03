#!/bin/bash

# 1. 스왑 메모리 해제
sudo swapoff -a
sudo sed -i '/swap/s/^/#/' /etc/fstab

# 2. 커널 모듈 로드
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

sudo modprobe overlay
sudo modprobe br_netfilter

# 3. 네트워크 설정
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sudo sysctl --system

# 4. 필수 패키지 설치
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl gpg

# 5. 쿠버네티스 저장소 키 다운로드 (v1.32)
sudo rm -f /etc/apt/keyrings/kubernetes-apt-keyring.gpg
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.32/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

# 6. 저장소 추가
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.32/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list

# 7. 툴 설치 (kubeadm, kubelet, containerd)
sudo apt-get update
sudo apt-get install -y kubelet kubeadm kubectl containerd
sudo apt-mark hold kubelet kubeadm kubectl

# 8. Containerd 설정
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml >/dev/null
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml

# 9. 서비스 시작
sudo systemctl restart containerd
sudo systemctl enable --now kubelet

echo "준비 완료! 이제 kubeadm join 명령어를 입력하세요."