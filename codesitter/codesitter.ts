import { tool } from "@opencode-ai/plugin"

declare const Bun: {
  spawn(command: string[], options: { stdout: "pipe"; stderr: "pipe" }): {
    stdout: ReadableStream<Uint8Array>
    stderr: ReadableStream<Uint8Array>
    exited: Promise<number>
  }
}

declare global {
  interface ImportMeta {
    dir: string
  }
}

const intents = [
  "find_declarations",
  "find_references",
  "find_callers",
  "find_callees",
  "get_file_outline",
  "find_importers",
] as const

export default tool({
  description: "Index source files and return code navigation results with evidence and coverage limits made explicit",
  args: {
    action: tool.schema.enum(["status", "index", "query"]).describe("codesitter command to run"),
    path: tool.schema.string().optional().describe("Project path. Defaults to the current opencode worktree or directory."),
    force: tool.schema.boolean().optional().describe("Allow indexing to run. Required by codesitter for index action."),
    lang: tool.schema.string().optional().describe("Comma-separated languages to index, for example: js,ts,tsx,go,java,rust,css,scss,sass"),
    files: tool.schema.string().array().optional().describe("Changed file paths relative to the project root. Omit to run full reconciliation."),
    deletedFiles: tool.schema.string().array().optional().describe("Deleted file paths relative to the project root for a targeted update."),
    intent: tool.schema.enum(intents).optional().describe("Query intent. Required for query action."),
    symbol: tool.schema.string().optional().describe("Symbol name for symbol-based query intents"),
    file: tool.schema.string().optional().describe("Relative file path for get_file_outline"),
    module: tool.schema.string().optional().describe("Module specifier for find_importers"),
    limit: tool.schema.number().int().min(1).max(200).optional().describe("Maximum query results (default 20, maximum 200)"),
  },
  async execute(args, context) {
    const projectPath = args.path || context.worktree || context.directory
    if (!projectPath) return JSON.stringify({ ok: false, error: { code: "INVALID_INPUT", detail: "path is required" } }, null, 2)

    const command: string[] = [args.action]
    if (args.action === "query") {
      if (!args.intent) return JSON.stringify({ ok: false, error: { code: "INVALID_INPUT", detail: "query action requires intent" } }, null, 2)
      command.push(args.intent)
    }

    command.push("--path", projectPath, "--json")

    if (args.action === "index") {
      if (args.force) command.push("--force")
      if (args.lang) command.push("--lang", args.lang)
      for (const file of args.files ?? []) command.push("--file", file)
      for (const file of args.deletedFiles ?? []) command.push("--deleted", file)
    }

    if (args.action === "query") {
      if (args.symbol) command.push("--symbol", args.symbol)
      if (args.file) command.push("--file", args.file)
      if (args.module) command.push("--module", args.module)
      if (args.limit) command.push("--limit", String(args.limit))
    }

    const codesitterBin = `${import.meta.dir}/golang/codesitter`
    const proc = Bun.spawn([codesitterBin, ...command], {
      stdout: "pipe",
      stderr: "pipe",
    })

    const [stdout, stderr] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
    ])
    const exitCode = await proc.exited

    const output = stdout.trim()
    if (exitCode === 0 && output) return output

    let parsed: Record<string, unknown> = {}
    if (output) {
      try {
        const value: unknown = JSON.parse(output)
        if (value && typeof value === "object" && !Array.isArray(value)) {
          parsed = value as Record<string, unknown>
        }
      } catch {
        // Preserve invalid/plain output in the normalized error details below.
      }
    }
    const detail = [stderr.trim(), output && !Object.keys(parsed).length ? output : ""]
      .filter(Boolean)
      .join("\n") || "codesitter returned no output"
    const error = parsed.error && typeof parsed.error === "object"
      ? parsed.error
      : { code: "CODESITTER_ERROR", detail }

    return JSON.stringify(
      {
        ...parsed,
        ok: false,
        command: typeof parsed.command === "string" ? parsed.command : args.action,
        path: typeof parsed.path === "string" ? parsed.path : projectPath,
        exit_code: exitCode,
        message: typeof parsed.message === "string" ? parsed.message : "codesitter command failed",
        error,
      },
      null,
      2,
    )
  },
})
