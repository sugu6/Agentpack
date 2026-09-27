import { describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@/lib/api', () => ({
  api: {
    market: {
      searchSkills: vi.fn(),
    },
  },
  ApiError: {
    from: (error: unknown) => ({ message: String(error) }),
  },
}))

import { api } from '@/lib/api'
import { useMarketStore } from './market'

describe('market store skill search errors', () => {
  it('clears the previous source error when clearing skills', async () => {
    setActivePinia(createPinia())
    vi.mocked(api.market.searchSkills).mockRejectedValueOnce(new Error('GitHub unavailable'))
    const store = useMarketStore()

    await store.searchSkills('', 30, 'github')
    expect(store.errorSkills).toBe('Error: GitHub unavailable')

    store.clearSkills()
    expect(store.errorSkills).toBeNull()
  })
})
