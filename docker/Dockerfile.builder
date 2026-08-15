FROM node:22-bookworm-slim

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=${HTTP_PROXY}
ENV HTTPS_PROXY=${HTTPS_PROXY}
ENV NO_PROXY=${NO_PROXY}

WORKDIR /source
COPY . .
RUN corepack enable \
  && corepack prepare pnpm@10.32.1 --activate \
  && pnpm install --frozen-lockfile \
  && pnpm build
