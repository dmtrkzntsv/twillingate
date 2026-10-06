import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { endpoints } from '@/lib/api'

/**
 * A share's 1x image as an object URL, for an `<img>` that cannot send the
 * console's token itself (the public image URL answers 404 once the share
 * is archived). The URL is revoked when the image changes or the component
 * unmounts. `failed` is true once the fetch has failed, so the caller can
 * show a placeholder.
 */
export function useShareImage(id: string): { url: string | undefined; failed: boolean } {
  const { data, isError } = useQuery({
    queryKey: ['widget-share-image', id],
    queryFn: () => endpoints.widgetShareImage(id),
    staleTime: Infinity,
    retry: false,
  })
  const [url, setUrl] = useState<string>()

  useEffect(() => {
    if (!data) return
    const objectUrl = URL.createObjectURL(data)
    setUrl(objectUrl)
    return () => {
      URL.revokeObjectURL(objectUrl)
      setUrl(undefined)
    }
  }, [data])

  return { url, failed: isError }
}
