sh나 py로 테스트 할때

`kubectl get svc` 이 명령어로 포트정보 확인해서 돌려야함

```
NAME              TYPE        CLUSTER-IP       EXTERNAL-IP   PORT(S)        AGE
gateway-service   NodePort    10.99.98.47      <none>        80:30407/TCP   20m
```

이렇게 출력될것