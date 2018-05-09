FROM golang:1.10
COPY ./ /go/src/github.com/robustperception/pushprox/
WORKDIR /go/src/github.com/robustperception/pushprox/proxy/
RUN go get -v \
  && CGO_ENABLED=0 GOOS=linux go build -v .

FROM debian:stable
COPY --from=0 /go/src/github.com/robustperception/pushprox/proxy/proxy /bin/proxy
EXPOSE 8080
USER nobody
CMD ["/bin/proxy"]