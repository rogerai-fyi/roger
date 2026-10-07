# RogerAI broker - the only deployed component. Tiny static binary.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# VERSION (optional): the release pipeline passes the tag; unset, the broker reports the release
# this source is (internal/buildinfo). It is the same one variable roger is stamped through.
ARG VERSION=
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath ${VERSION:+-ldflags "-X rogerai.fm/roger/v6/internal/buildinfo.Version=${VERSION}"} -o /rogerai-broker ./cmd/rogerai-broker

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /rogerai-broker /rogerai-broker
# DO App Platform injects $PORT; the broker binds 0.0.0.0:$PORT automatically.
EXPOSE 8080
ENTRYPOINT ["/rogerai-broker"]
