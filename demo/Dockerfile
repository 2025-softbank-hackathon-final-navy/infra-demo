FROM golang:alpine AS builder

WORKDIR /app

RUN go mod init serverless-gateway || true && \
    go get github.com/gin-gonic/gin && \
    go get k8s.io/client-go@v0.28.2 && \
    go get k8s.io/api@v0.28.2 && \
    go get k8s.io/apimachinery@v0.28.2

COPY main.go .

RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -o gateway main.go

FROM alpine:latest
WORKDIR /root/
COPY --from=builder /app/gateway .

EXPOSE 80

CMD ["./gateway"]