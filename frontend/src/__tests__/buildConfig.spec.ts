// @vitest-environment node
import { describe, expect, it } from 'vitest'
import viteConfig from '../../vite.config'

async function configFor(command: 'build' | 'serve') {
  if (typeof viteConfig !== 'function') {
    throw new Error('Expected a command-aware Vite configuration')
  }
  return viteConfig({ command, mode: command === 'build' ? 'production' : 'development' })
}

describe('type checking ownership', () => {
  it('does not start a second checker during production bundling', async () => {
    const config = await configFor('build')
    expect(config.plugins).not.toContainEqual(expect.objectContaining({ name: 'vite-plugin-checker' }))
  })

  it('retains live checking in the development server', async () => {
    const config = await configFor('serve')
    expect(config.plugins).toContainEqual(expect.objectContaining({ name: 'vite-plugin-checker' }))
  })
})
