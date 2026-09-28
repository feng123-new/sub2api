import { apiClient } from '../client'

export interface GeneratedImage {
  id: string
  user_id: number
  user_email: string
  group_id: number
  group_name: string
  api_key_id: number
  account_id: number
  model: string
  request_id: string
  endpoint: string
  mime_type: string
  byte_size: number
  width: number
  height: number
  created_at: string
  expires_at: string
}

export interface GeneratedImageFilters {
  user_id?: number
  model?: string
  start_date?: string
  end_date?: string
}

export interface GeneratedImagePage {
  items: GeneratedImage[]
  total: number
  page: number
  page_size: number
  retention_days: number
}

export async function list(
  params: GeneratedImageFilters & { page: number; page_size: number },
  signal?: AbortSignal
): Promise<GeneratedImagePage> {
  const { data } = await apiClient.get<GeneratedImagePage>('/admin/generated-images', { params, signal })
  return data
}

export async function content(id: string, signal?: AbortSignal): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(`/admin/generated-images/${encodeURIComponent(id)}/content`, {
    responseType: 'blob',
    signal
  })
  return data
}

export async function remove(id: string, signal?: AbortSignal): Promise<void> {
  await apiClient.delete(`/admin/generated-images/${encodeURIComponent(id)}`, { signal })
}

export const generatedImagesAPI = { list, content, remove }
export default generatedImagesAPI
