FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /app ./examples/shop/app

FROM alpine:3.20
COPY --from=build /app /app
ENTRYPOINT ["/app"]
