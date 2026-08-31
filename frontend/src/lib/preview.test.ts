import { describe, expect, it } from 'vitest'
import { previewKindFor } from './preview'

describe('previewKindFor', () => {
  it('picks a renderer for the types a browser handles natively', () => {
    expect(previewKindFor('image/png')).toBe('image')
    expect(previewKindFor('image/jpeg')).toBe('image')
    expect(previewKindFor('video/mp4')).toBe('video')
    expect(previewKindFor('audio/mpeg')).toBe('audio')
    expect(previewKindFor('application/pdf')).toBe('pdf')
    expect(previewKindFor('text/plain')).toBe('text')
    expect(previewKindFor('text/markdown')).toBe('text')
    expect(previewKindFor('text/csv')).toBe('text')
    expect(previewKindFor('application/json')).toBe('text')
  })

  it('ignores parameters and case', () => {
    expect(previewKindFor('IMAGE/PNG')).toBe('image')
    expect(previewKindFor('text/plain; charset=utf-8')).toBe('text')
    expect(previewKindFor('  image/png  ')).toBe('image')
  })

  /**
   * An SVG is a script-bearing document. The download response carries a
   * sandbox Content-Security-Policy that would stop it running, but the safer
   * answer is not to hand it to a renderer at all — so this must never be
   * classified as an image.
   */
  it('refuses to render SVG as an image', () => {
    expect(previewKindFor('image/svg+xml')).toBe('none')
    expect(previewKindFor('IMAGE/SVG+XML')).toBe('none')
    expect(previewKindFor('image/svg+xml; charset=utf-8')).toBe('none')
  })

  it('offers no preview for anything it does not recognise', () => {
    expect(previewKindFor('application/octet-stream')).toBe('none')
    expect(previewKindFor('application/x-msdownload')).toBe('none')
    expect(previewKindFor(undefined)).toBe('none')
    expect(previewKindFor('')).toBe('none')
  })

  /**
   * The server sends anything outside its inline allowlist as an attachment.
   * Offering a preview for one of those would open a dialog that stays blank
   * while the browser quietly starts a download.
   */
  it('offers no preview for types the server will not serve inline', () => {
    // HTML is a script-bearing document; the backend refuses it inline.
    expect(previewKindFor('text/html')).toBe('none')
    expect(previewKindFor('application/xml')).toBe('none')
    expect(previewKindFor('text/x-shellscript')).toBe('none')
  })
})
