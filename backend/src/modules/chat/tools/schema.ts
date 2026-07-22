import Ajv, { type ErrorObject, type ValidateFunction } from 'ajv'

interface AjvInstance {
  compile: (schema: Record<string, unknown>) => ValidateFunction
}

const AjvConstructor = Ajv as unknown as new (options: Record<string, unknown>) => AjvInstance
const schemaValidator = new AjvConstructor({
  allErrors: true,
  strict: true,
  validateSchema: true,
  allowUnionTypes: false
})

export interface CompiledChatToolSchema {
  validate: ValidateFunction
}

export function compileChatToolSchema(schema: Record<string, unknown>): CompiledChatToolSchema {
  assertObjectSchema(schema)
  return { validate: schemaValidator.compile(schema) }
}

export function validateChatToolArguments(
  compiled: CompiledChatToolSchema,
  argumentsJson: string,
  maxArgumentBytes: number
): Record<string, unknown> {
  if (Buffer.byteLength(argumentsJson, 'utf8') > maxArgumentBytes) {
    throw new ChatToolSchemaError('tool_arguments_too_large', `工具参数超过 ${maxArgumentBytes} 字节上限`)
  }
  let value: unknown
  try {
    value = JSON.parse(argumentsJson)
  } catch {
    throw new ChatToolSchemaError('tool_arguments_invalid_json', '工具参数不是有效 JSON')
  }
  if (!compiled.validate(value)) {
    throw new ChatToolSchemaError('tool_arguments_invalid', `工具参数无效：${summarizeAjvErrors(compiled.validate.errors)}`)
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new ChatToolSchemaError('tool_arguments_invalid', '工具参数无效：根值必须是对象')
  }
  return value as Record<string, unknown>
}

export class ChatToolSchemaError extends Error {
  constructor(public readonly code: string, message: string) {
    super(message)
    this.name = 'ChatToolSchemaError'
  }
}

function assertObjectSchema(schema: Record<string, unknown>): void {
  if (schema.type !== 'object') throw new Error('内部工具 inputSchema 根类型必须是 object')
  if (schema.additionalProperties !== false) throw new Error('内部工具 inputSchema 必须显式禁止 additionalProperties')
}

function summarizeAjvErrors(errors: ErrorObject[] | null | undefined): string {
  if (!errors?.length) return '未通过 Schema 校验'
  return errors.slice(0, 4).map((error) => {
    const path = error.instancePath || '/'
    return `${path} ${error.message ?? error.keyword}`
  }).join('；')
}
