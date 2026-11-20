FROM golang:1.24.13-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY *.go schemas.json prompt.txt ./
RUN CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /slides-api .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates poppler-utils \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --no-create-home slides
WORKDIR /app
COPY --from=build /slides-api /app/slides-api
USER slides
EXPOSE 8280
ENTRYPOINT ["/app/slides-api"]
