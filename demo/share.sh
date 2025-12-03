DOCKER_BUILDKIT=1 docker build --no-cache -t gateway .

docker save -o gateway.tar gateway:latest

scp gateway.tar gyungdal@10.2.0.204:~/
scp gateway.tar gyungdal@10.2.0.205:~/

# worker
sudo ctr -n k8s.io images import gateway.tar
sudo ctr -n k8s.io images tag docker.io/library/gateway:latest localhost/gateway:latest
sudo crictl images | grep gateway

# kubectl get rs
# kubectl delete rs func-d3f2979475-698d7d5bf8
# kubectl rollout restart deployment gateway

# kubectl get svc 해서 나온 IP:PORT로 테스트 스크립트 돌려야함

# kubectl logs -l app=gateway-server -f