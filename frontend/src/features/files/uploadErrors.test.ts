import { describe, expect, it } from 'vitest'
import { ApiError } from '@/api/client'
import { describeUploadError } from './uploadQueue'

/**
 * How an upload failure is explained, and whether retrying is worth offering.
 *
 * The distinction matters more than the wording: offering "Retry" for a quota
 * failure invites somebody to click it forever, and *not* offering it for a
 * transient one makes them re-drag the file.
 */
describe('describeUploadError', () => {
  function apiError(code: string, message: string, retryAfter?: number): ApiError {
    return new ApiError(
      400,
      { code: code as never, message },
      undefined,
      undefined,
      retryAfter,
    )
  }

  it('does not offer a retry for anything retrying cannot fix', () => {
    const permanent = [
      apiError('quota_exceeded', 'Not enough space.'),
      apiError('payload_too_large', 'Too big.'),
      apiError('conflict', 'Name taken.'),
      apiError('validation_failed', 'The file matched a malware signature: Eicar-Test-Signature'),
      apiError('forbidden', 'No.'),
      apiError('unauthorized', 'Signed out.'),
    ]
    for (const error of permanent) {
      expect(describeUploadError(error).retryable, error.code).toBe(false)
    }
  })

  // A malware refusal is a validation failure, and the server's message names
  // the signature — which is what lets somebody tell a false positive from a
  // real detection, so it must not be replaced with a generic sentence.
  it('passes a malware refusal through with its signature intact', () => {
    const error = apiError(
      'validation_failed',
      'the file matched a malware signature: Eicar-Test-Signature',
    )
    const { message, retryable } = describeUploadError(error)

    expect(message).toContain('Eicar-Test-Signature')
    expect(retryable).toBe(false)
  })

  it('offers a retry for a rate limit, and says how long to wait', () => {
    const { message, retryable } = describeUploadError(
      apiError('rate_limited', 'Too many requests.', 30),
    )
    expect(retryable).toBe(true)
    expect(message).toContain('30 seconds')
  })

  it('falls back to a vaguer wait when the server did not say', () => {
    const { message, retryable } = describeUploadError(apiError('rate_limited', 'Slow down.'))
    expect(retryable).toBe(true)
    expect(message).toMatch(/wait a moment/i)
  })

  // Nothing is wrong with the file when the scanner is unreachable, so the
  // client should try again rather than making the user re-drag it.
  it('offers a retry when the server is temporarily unavailable', () => {
    const { message, retryable } = describeUploadError(
      apiError('service_unavailable', 'Uploads are paused because the malware scanner cannot be reached.'),
    )
    expect(retryable).toBe(true)
    expect(message).toContain('malware scanner')
  })

  it('treats an unrecognised failure as worth retrying', () => {
    expect(describeUploadError(new Error('socket closed')).retryable).toBe(true)
  })
})
