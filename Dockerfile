FROM golang:1.21 as build-env

WORKDIR /go/src/app
COPY . /go/src/app

RUN make
RUN strip gorestapicmd

FROM gcr.io/distroless/static

COPY --from=build-env /go/src/app/gorestapicmd /app/blocksrv
COPY --from=build-env /go/src/app/embed/public_html /app/embed
CMD ["/app/blocksrv","api"]
