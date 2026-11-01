import { toast } from 'sonner'
import { errorMessage } from './api'

/** Shows a failed call's `errorMessage` as an error toast. */
export function toastError(err: unknown): void {
  toast.error(errorMessage(err))
}
