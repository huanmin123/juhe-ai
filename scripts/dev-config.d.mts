export declare function resolveDevelopmentAutoLoginUsername(value: string | undefined): string | undefined

type DevelopmentEnvironment = Record<string, string | undefined>

export declare function resolveDevelopmentBackendTarget(
  processEnv: DevelopmentEnvironment,
  frontendEnv: DevelopmentEnvironment,
  backendEnv: DevelopmentEnvironment
): string
