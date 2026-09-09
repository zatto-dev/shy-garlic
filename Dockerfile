# Shy Garlic — jedna statyczna binarka, obraz bez systemu operacyjnego.
# Cel z SPEC: poniżej 25 MB. Frontend, ikony i szablony siedzą w embed.FS,
# więc w obrazie nie ma ani node_modules, ani plików do zgubienia.

FROM golang:1.25-alpine AS build
WORKDIR /src

# najpierw zależności — warstwa cache'uje się między buildami
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.wersja=${VERSION}" \
      -o /out/shyg ./cmd/shyg

# pusty katalog do przeniesienia — distroless nie ma ani RUN, ani chown
RUN mkdir -p /puste

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/shyg /shyg
COPY --from=build /src/examples/homelab.yaml /etc/shygarlic/shygarlic.yaml
# ! Bez tego katalogu baza lądowała w `/`, którego uid 65532 nie zapisze, a
# nazwany wolumen zamontowany na nieistniejącą ścieżkę powstaje jako root:root.
COPY --from=build --chown=65532:65532 /puste /data

USER nonroot:nonroot
EXPOSE 8080
ENV SHYG_CONFIG=/etc/shygarlic/shygarlic.yaml
# baza w /data, a obok niej podkatalogi icons/ i bg/ — nigdy w jednym katalogu,
# bo tapeta o nazwie `shygarlic.db` nadpisałaby bazę
ENV SHYG_DATA=/data/shygarlic.db

ENTRYPOINT ["/shyg"]
CMD ["serve", "-config", "/etc/shygarlic/shygarlic.yaml", "-addr", ":8080"]

# ── wariant z gitem ──────────────────────────────────────────────────────────
# Obraz distroless nie ma powłoki ani gita, więc commity configu w nim nie
# zadziałają. Kto ich chce, buduje ten target:
#   docker build --target git -t shy-garlic:git .
FROM alpine:3.21 AS git
RUN apk add --no-cache git ca-certificates tzdata && adduser -D -u 65532 nonroot \
 && mkdir -p /data && chown 65532:65532 /data
COPY --from=build /out/shyg /shyg
COPY --from=build /src/examples/homelab.yaml /etc/shygarlic/shygarlic.yaml
USER nonroot
EXPOSE 8080
ENV SHYG_CONFIG=/etc/shygarlic/shygarlic.yaml
ENV SHYG_DATA=/data/shygarlic.db
ENTRYPOINT ["/shyg"]
CMD ["serve", "-config", "/etc/shygarlic/shygarlic.yaml", "-addr", ":8080"]
