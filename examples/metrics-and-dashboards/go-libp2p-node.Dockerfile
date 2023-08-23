FROM golang:alpine
WORKDIR /app
COPY ./main.go .
RUN go mod init example.com/m/v2
RUN go mod tidy
RUN go build main.go
ENTRYPOINT [ "/app/main" ]
