DOCKER_BUILDKIT=1 docker build --no-cache -t py-runner:latest .

docker save -o py-runner.tar py-runner:latest

scp py-runner.tar gyungdal@10.2.0.204:~/
scp py-runner.tar gyungdal@10.2.0.205:~/

# worker
sudo ctr -n k8s.io images import py-runner.tar
sudo ctr -n k8s.io images tag docker.io/library/py-runner:latest localhost/py-runner:latest
sudo crictl images | grep runner