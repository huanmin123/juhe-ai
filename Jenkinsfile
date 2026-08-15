pipeline {
  agent any

  options {
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
    skipDefaultCheckout(true)
  }

  parameters {
    booleanParam(name: 'DEPLOY_PROD', defaultValue: false, description: '仅手动运行：将已验证的 test 三镜像晋级到 prod。')
  }

  environment {
    HARBOR_REPOSITORY_NODE = 'platform/juhe-ai'
    HARBOR_REPOSITORY_JOBS = 'platform/juhe-ai-go-jobs'
    HARBOR_REPOSITORY_GATEWAY = 'platform/juhe-ai-go-gateway'
    HARBOR_REGISTRY_FILE = '/run/jenkins-secrets/harbor-registry'
    GITEE_WRITE_KEY = '/run/jenkins-secrets/gitee-k8s-write'
    RELEASE_OBSERVER_KUBECONFIG = '/run/jenkins-secrets/kubeconfig-release-observer'
    PLATFORM_REPOSITORY = 'git@gitee.com:huanminabc/k8s-juhe.git'
    RELEASE_BRANCH = 'main'
    INGRESS_ENDPOINT = 'http://192.168.1.76:32080'
    BUILD_HTTP_PROXY = 'http://10.66.45.2:7890'
    BUILD_NO_PROXY = '127.0.0.1,localhost,192.168.1.76,192.168.1.203,10.66.45.2'
  }

  stages {
    stage('检出源码') {
      steps {
        checkout scm
        script {
          env.SOURCE_COMMIT = sh(script: 'git rev-parse --short=12 HEAD', returnStdout: true).trim()
          env.SOURCE_SUBJECT = sh(script: 'git log -1 --pretty=%s', returnStdout: true).trim()
          env.HARBOR_REGISTRY = readFile(env.HARBOR_REGISTRY_FILE).trim()
          if (!validRegistry(env.HARBOR_REGISTRY)) {
            error 'HARBOR_REGISTRY 必须是 host 或 host:port。'
          }
          if (!fileExists(env.GITEE_WRITE_KEY)) {
            error '缺少平台 GitOps 发布状态写入密钥。'
          }
        }
      }
    }

    stage('构建前端与 Node 产物') {
      when { expression { !params.DEPLOY_PROD } }
      steps {
        sh '''#!/bin/sh
          set -eu
          docker run --rm --network host \
            -e HTTP_PROXY="$BUILD_HTTP_PROXY" -e HTTPS_PROXY="$BUILD_HTTP_PROXY" -e NO_PROXY="$BUILD_NO_PROXY" \
            -v "$PWD:/source" -w /source node:22-bookworm-slim sh -eu -c \
            'corepack enable && corepack prepare pnpm@10.32.1 --activate && pnpm install --frozen-lockfile && pnpm build'
        '''
      }
    }

    stage('构建并推送三镜像') {
      when { expression { !params.DEPLOY_PROD } }
      steps {
        script {
          env.NODE_IMAGE = "${env.HARBOR_REGISTRY}/${env.HARBOR_REPOSITORY_NODE}:${env.SOURCE_COMMIT}"
          env.JOBS_IMAGE = "${env.HARBOR_REGISTRY}/${env.HARBOR_REPOSITORY_JOBS}:${env.SOURCE_COMMIT}"
          env.GATEWAY_IMAGE = "${env.HARBOR_REGISTRY}/${env.HARBOR_REPOSITORY_GATEWAY}:${env.SOURCE_COMMIT}"
        }
        withCredentials([usernamePassword(credentialsId: 'harbor-platform-push', usernameVariable: 'HARBOR_USERNAME', passwordVariable: 'HARBOR_PASSWORD')]) {
          sh '''#!/bin/sh
            set -eu
            printf '%s' "$HARBOR_PASSWORD" | docker login "$HARBOR_REGISTRY" --username "$HARBOR_USERNAME" --password-stdin
            build_args="--network host --build-arg HTTP_PROXY=$BUILD_HTTP_PROXY --build-arg HTTPS_PROXY=$BUILD_HTTP_PROXY --build-arg NO_PROXY=$BUILD_NO_PROXY"
            docker build $build_args --tag "$NODE_IMAGE" --file docker/Dockerfile .
            docker build $build_args --build-arg GO_PROJECT=jobs --tag "$JOBS_IMAGE" --file docker/Dockerfile.go-project .
            docker build $build_args --build-arg GO_PROJECT=gateway --tag "$GATEWAY_IMAGE" --file docker/Dockerfile.go-project .
            docker push "$NODE_IMAGE"
            docker push "$JOBS_IMAGE"
            docker push "$GATEWAY_IMAGE"
            for image in "$NODE_IMAGE" "$JOBS_IMAGE" "$GATEWAY_IMAGE"; do
              digest=$(docker image inspect --format='{{index .RepoDigests 0}}' "$image")
              case "$digest" in *@sha256:*) printf '%s\n' "${digest##*@}" ;; *) echo "无法取得 $image 的不可变 digest" >&2; exit 1 ;; esac
            done > .juhe-ai-digests
          '''
        }
        script {
          def digests = readFile('.juhe-ai-digests').readLines()
          if (digests.size() != 3 || digests.any { !validDigest(it) }) {
            error '三镜像 digest 不完整或格式错误。'
          }
          env.NODE_DIGEST = digests[0]
          env.JOBS_DIGEST = digests[1]
          env.GATEWAY_DIGEST = digests[2]
        }
      }
    }

    stage('写入 test release state') {
      when { expression { !params.DEPLOY_PROD } }
      steps {
        script {
          writeReleaseState('test', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, 'jenkins-ci')
        }
      }
    }

    stage('验证 test') {
      when { expression { !params.DEPLOY_PROD } }
      steps {
        script {
          waitForIngress('test')
          markReleaseVerified('test', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST)
        }
      }
    }

    stage('读取已验证 test') {
      when { expression { params.DEPLOY_PROD } }
      steps {
        script {
          def release = readVerifiedTestRelease()
          env.SOURCE_COMMIT = release.sourceCommit
          env.NODE_DIGEST = release.nodeDigest
          env.JOBS_DIGEST = release.jobsDigest
          env.GATEWAY_DIGEST = release.gatewayDigest
        }
      }
    }

    stage('写入 prod 晋级状态') {
      when { expression { params.DEPLOY_PROD } }
      steps {
        script {
          def release = readVerifiedTestRelease()
          if (release.sourceCommit != env.SOURCE_COMMIT || release.nodeDigest != env.NODE_DIGEST || release.jobsDigest != env.JOBS_DIGEST || release.gatewayDigest != env.GATEWAY_DIGEST) {
            error 'test release state 在晋级期间发生变化，拒绝写入 prod。'
          }
          writeReleaseState('prod', env.SOURCE_COMMIT, env.NODE_DIGEST, env.JOBS_DIGEST, env.GATEWAY_DIGEST, 'jenkins-prod-promotion')
        }
      }
    }

    stage('验证 prod') {
      when { expression { params.DEPLOY_PROD } }
      steps {
        script { waitForIngress('prod') }
      }
    }
  }
}

def validRegistry(value) { return value ==~ /^[A-Za-z0-9.-]+(?::[0-9]{1,5})?$/ }
def validDigest(value) { return value ==~ /^sha256:[a-f0-9]{64}$/ }
def validCommit(value) { return value ==~ /^[a-f0-9]{7,40}$/ }

def releaseWorkspace() { return "${env.WORKSPACE}/.platform-release" }

def refreshPlatformReleaseWorkspace() {
  sh "rm -rf '${releaseWorkspace()}' && GIT_SSH_COMMAND=\"ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts\" git clone --depth 1 --branch '${env.RELEASE_BRANCH}' '${env.PLATFORM_REPOSITORY}' '${releaseWorkspace()}'"
}

def metadataValue(environmentName, key) {
  def file = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}/release-metadata.yaml"
  def value = sh(script: "sed -n 's/^  ${key}: \"\(.*\)\"/\\1/p' '${file}' | head -n1", returnStdout: true).trim()
  if (!value) error "${environmentName} release state 缺少 ${key}。"
  return value
}

def readVerifiedTestRelease() {
  refreshPlatformReleaseWorkspace()
  def release = [
    sourceCommit: metadataValue('test', 'sourceCommit'),
    nodeDigest: metadataValue('test', 'nodeImageDigest'),
    jobsDigest: metadataValue('test', 'jobsImageDigest'),
    gatewayDigest: metadataValue('test', 'gatewayImageDigest'),
    status: metadataValue('test', 'verification.status'),
    verifiedCommit: metadataValue('test', 'verification.sourceCommit')
  ]
  if (!validCommit(release.sourceCommit) || !validDigest(release.nodeDigest) || !validDigest(release.jobsDigest) || !validDigest(release.gatewayDigest) || release.status != 'passed' || release.verifiedCommit != release.sourceCommit) {
    error 'test release state 未通过完整性与验证门禁。'
  }
  return release
}

def replaceDigest(file, imageName, digest) {
  def expression = '''s{(- name: IMAGE\n\s+newName: [^\n]+\n\s+digest: )sha256:[a-f0-9]{64}}{$1DIGEST}'''
    .replace('IMAGE', imageName)
    .replace('DIGEST', digest)
  sh "perl -0pi -e '${expression}' '${file}'"
}

def writeReleaseState(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest, actor) {
  if (!validCommit(sourceCommit) || !validDigest(nodeDigest) || !validDigest(jobsDigest) || !validDigest(gatewayDigest)) error '发布状态字段不合法。'
  refreshPlatformReleaseWorkspace()
  def overlay = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}"
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai', nodeDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-jobs', jobsDigest)
  replaceDigest("${overlay}/kustomization.yaml", 'juhe-ai-go-gateway', gatewayDigest)
  sh """#!/bin/sh
    set -eu
    cd '${releaseWorkspace()}'
    sed -i \\
      -e 's|^  sourceCommit: ".*"|  sourceCommit: "${sourceCommit}"|' \\
      -e 's|^  nodeImageDigest: ".*"|  nodeImageDigest: "${nodeDigest}"|' \\
      -e 's|^  jobsImageDigest: ".*"|  jobsImageDigest: "${jobsDigest}"|' \\
      -e 's|^  gatewayImageDigest: ".*"|  gatewayImageDigest: "${gatewayDigest}"|' \\
      -e 's|^  releaseActor: ".*"|  releaseActor: "${actor}"|' \\
      -e 's|^  verification.status: ".*"|  verification.status: "pending"|' \\
      -e 's|^  verification.sourceCommit: ".*"|  verification.sourceCommit: ""|' \\
      '${overlay}/release-metadata.yaml'
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${overlay}/kustomization.yaml' '${overlay}/release-metadata.yaml'
    git commit -m '[skip ci] release(juhe-ai-${environmentName}): ${sourceCommit}'
    GIT_SSH_COMMAND="ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts" git push origin HEAD:'${env.RELEASE_BRANCH}'
  """
}

def waitForIngress(environmentName) {
  def host = environmentName == 'test' ? 'test.aijh.huanmin.top' : 'aijh.huanmin.top'
  sh """#!/bin/sh
    set -eu
    i=0
    while [ \$i -lt 60 ]; do
      if env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY curl --fail --silent --show-error --max-time 10 -H 'Host: ${host}' '${env.INGRESS_ENDPOINT}/__aisys__/api/health' >/dev/null; then exit 0; fi
      i=\$((i + 1)); sleep 5
    done
    echo '${environmentName} 入口或 Node DB-ready health 未通过。' >&2
    exit 1
  """
}

def markReleaseVerified(environmentName, sourceCommit, nodeDigest, jobsDigest, gatewayDigest) {
  refreshPlatformReleaseWorkspace()
  def file = "${releaseWorkspace()}/apps/juhe-ai/overlays/${environmentName}/release-metadata.yaml"
  if (metadataValue(environmentName, 'sourceCommit') != sourceCommit || metadataValue(environmentName, 'nodeImageDigest') != nodeDigest || metadataValue(environmentName, 'jobsImageDigest') != jobsDigest || metadataValue(environmentName, 'gatewayImageDigest') != gatewayDigest) error 'release state 在验证期间变化。'
  sh """#!/bin/sh
    set -eu
    sed -i -e 's|^  verification.status: ".*"|  verification.status: "passed"|' -e 's|^  verification.sourceCommit: ".*"|  verification.sourceCommit: "${sourceCommit}"|' '${file}'
    cd '${releaseWorkspace()}'
    git config user.name platform-jenkins
    git config user.email jenkins@jh.huanmin.top
    git add '${file}'
    git commit -m '[skip ci] release(juhe-ai-${environmentName}): ${sourceCommit} verified'
    GIT_SSH_COMMAND="ssh -i '${env.GITEE_WRITE_KEY}' -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=/usr/share/jenkins/ref/gitee-known-hosts" git push origin HEAD:'${env.RELEASE_BRANCH}'
  """
}
