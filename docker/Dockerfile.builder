ARG NODE_BUILDER_PNPM_IMAGE
FROM ${NODE_BUILDER_PNPM_IMAGE}

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
  pnpm install --frozen-lockfile --store-dir=/pnpm/store
ARG VITE_JUHE_AI_BUILD_ID
ENV VITE_JUHE_AI_BUILD_ID=${VITE_JUHE_AI_BUILD_ID}
# The current production release keeps Go J3b/model-check routes disabled.
# A future dedicated J3b release must change this explicitly in its reviewed
# release contract; never inherit a builder host's ambient Vite environment.
ARG VITE_JUHE_AI_J3B_ENABLED=false
ENV VITE_JUHE_AI_J3B_ENABLED=${VITE_JUHE_AI_J3B_ENABLED}
COPY . .
RUN pnpm build
