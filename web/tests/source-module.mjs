import { Buffer } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import ts from 'typescript'

let sequence = 0

// Exercise the real module with explicit browser/transport dependencies in Node.
export async function sourceModule(path, dependencies) {
  const id = ++sequence
  const fixtures = (globalThis.__frontendTestDependencies ??= new Map())
  fixtures.set(id, dependencies)
  const source = await readFile(path, 'utf8')
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext },
  })
  const code = outputText.replace(
    /import\s*\{([^}]+)\}\s*from\s*['"]([^'"]+)['"];?/g,
    (_, bindings, module) => {
      if (!Object.hasOwn(dependencies, module)) throw new Error(`Missing fixture: ${module}`)
      return `const { ${bindings.replace(/\bas\b/g, ':')} } = globalThis.__frontendTestDependencies.get(${id})[${JSON.stringify(module)}]`
    },
  )
  try {
    return await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}#${id}`)
  } finally {
    fixtures.delete(id)
  }
}
