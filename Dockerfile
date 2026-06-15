# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY *.go ./
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/tablo-homerun-proxy .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && addgroup -S app \
    && adduser -S -G app app
WORKDIR /app
COPY --from=build /out/tablo-homerun-proxy /usr/local/bin/tablo-homerun-proxy
RUN mkdir -p /data && chown -R app:app /data
USER app
EXPOSE 8181/tcp 1900/udp 65001/udp
VOLUME ["/data"]
ENTRYPOINT ["tablo-homerun-proxy"]
CMD ["--outdir", "/data"]
