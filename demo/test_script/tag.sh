kubectl label nodes softbank-slave-1 mem_type=small --overwrite

kubectl label nodes softbank-slave-2 mem_type=large --overwrite

kubectl get nodes --show-labels