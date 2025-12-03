#!/bin/bash

# 1. 스왑 메모리 해제 (쿠버네티스 필수)
sudo swapoff -a
sudo sed -i '/swap/s/^/#/' /etc/fstab

# 2. 커널 모듈 로드
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

sudo modprobe overlay
sudo modprobe br_netfilter

# 3. 네트워크 설정 (브리지 패킷 허용)
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
# (이전 파일이 있다면 삭제 후 진행)
sudo rm -f /etc/apt/keyrings/kubernetes-apt-keyring.gpg
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.32/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg

# 6. 저장소 추가
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.32/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list

# 7. 패키지 목록 업데이트 및 설치 (이 부분이 빠져서 에러가 났었습니다)
sudo apt-get update
sudo apt-get install -y kubelet kubeadm kubectl containerd
sudo apt-mark hold kubelet kubeadm kubectl

# 8. Containerd 설정 (SystemdCgroup 활성화)
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml >/dev/null
# SystemdCgroup을 true로 변경
sudo sed -i 's/SystemdCgroup = false/SystemdCgroup = true/g' /etc/containerd/config.toml

# 9. 서비스 재시작
sudo systemctl restart containerd
sudo systemctl enable --now kubelet

# 10. 쿠버네티스 클러스터 초기화 (Calico 네트워크 대역 사용)
# 주의: 이미 초기화된 적이 있다면 sudo kubeadm reset 명령어가 필요할 수 있습니다.
sudo kubeadm init --pod-network-cidr=192.168.0.0/16

# 11. 관리자 설정 (kubectl 사용 권한 부여)
mkdir -p $HOME/.kube
sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config

# 12. CNI (Calico) 설치
kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.27.0/manifests/calico.yaml

echo "=========================================================="
echo "설치 완료! 위쪽 로그에서 'kubeadm join ...' 명령어를 복사해서"
echo "워커 노드(Slave)에서 실행하세요."
echo "=========================================================="