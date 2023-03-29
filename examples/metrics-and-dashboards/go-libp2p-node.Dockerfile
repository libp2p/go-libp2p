FROM golang:alpine
WORKDIR /app
# COPY ./go.mod .
# COPY ./go.sum .
COPY ./main.go .
RUN go mod init example.com/m/v2
RUN go mod tidy
RUN go build main.go
ENTRYPOINT [ "/app/main" ]