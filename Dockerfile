# Build the core API binary.
FROM golang:1.25-alpine AS build
WORKDIR /src
# Dependencies are vendored so the image builds fully offline.
COPY . .
ARG TARGET=./cmd/api
RUN CGO_ENABLED=0 go build -mod=vendor -o /out/app ${TARGET}

FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/app /usr/local/bin/app
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/app"]
