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
  description: "Run codesitter status, indexing, and semantic code queries for a project",
  args: {
    action: tool.schema.enum(["status", "index", "query"]).describe("codesitter command to run"),
    path: tool.schema.string().optional().describe("Project path. Defaults to the current opencode worktree or directory."),
    force: tool.schema.boolean().optional().describe("Allow indexing to run. Required by codesitter for index action."),
    lang: tool.schema.string().optional().describe("Comma-separated language filters for indexing, for example: js,ts,tsx,go,java,rust,css,scss,sass"),
    include: tool.schema.string().optional().describe("Include glob for indexing"),
    exclude: tool.schema.string().optional().describe("Additional exclude glob for indexing; common dependency and build directories are skipped by default."),
    maxFiles: tool.schema.number().int().positive().optional().describe("Maximum files to index"),
    intent: tool.schema.enum(intents).optional().describe("Query intent. Required for query action."),
    symbol: tool.schema.string().optional().describe("Symbol name for symbol-based query intents"),
    file: tool.schema.string().optional().describe("Relative file path for get_file_outline"),
    module: tool.schema.string().optional().describe("Module specifier for find_importers"),
    limit: tool.schema.number().int().positive().optional().describe("Maximum query results"),
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
      if (args.include) command.push("--include", args.include)
      if (args.exclude) command.push("--exclude", args.exclude)
      if (args.maxFiles) command.push("--max-files", String(args.maxFiles))
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

    const stdout = await new Response(proc.stdout).text()
    const stderr = await new Response(proc.stderr).text()
    const exitCode = await proc.exited

    if (stdout.trim()) return stdout.trim()

    return JSON.stringify(
      {
        ok: exitCode === 0,
        command: args.action,
        path: projectPath,
        exit_code: exitCode,
        error: stderr.trim() ? { code: "CODESITTER_ERROR", detail: stderr.trim() } : undefined,
      },
      null,
      2,
    )
  },
})
