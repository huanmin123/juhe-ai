ARG NODE_BUILDER_IMAGE
FROM ${NODE_BUILDER_IMAGE}

ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=${HTTP_PROXY}
ENV HTTPS_PROXY=${HTTPS_PROXY}
ENV NO_PROXY=${NO_PROXY}
ENV PNPM_HOME=/pnpm
ENV PATH=${PNPM_HOME}:${PATH}

WORKDIR /source
COPY .npmrc package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY backend/package.json backend/package.json
COPY frontend/package.json frontend/package.json
RUN --mount=type=cache,id=juhe-ai-pnpm-store,target=/pnpm/store \
  corepack enable \
  && corepack prepare pnpm@10.32.1 --activate \
  && pnpm install --frozen-lockfile --store-dir=/pnpm/store
ARG VITE_JUHE_AI_BUILD_ID
ENV VITE_JUHE_AI_BUILD_ID=${VITE_JUHE_AI_BUILD_ID}
COPY . .
RUN pnpm build
