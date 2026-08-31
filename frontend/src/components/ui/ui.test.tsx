import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { Alert } from './alert'
import { Badge } from './badge'
import { Button } from './button'
import { Card, CardContent, CardHeader, CardTitle } from './card'

describe('Button', () => {
  it('defaults to type="button" so it cannot submit a form by accident', () => {
    render(<Button>Save</Button>)
    expect(screen.getByRole('button', { name: 'Save' })).toHaveAttribute('type', 'button')
  })

  it('honours an explicit type', () => {
    render(<Button type="submit">Submit</Button>)
    expect(screen.getByRole('button', { name: 'Submit' })).toHaveAttribute('type', 'submit')
  })

  it('calls its handler on click', async () => {
    const onClick = vi.fn()
    const user = userEvent.setup()
    render(<Button onClick={onClick}>Go</Button>)

    await user.click(screen.getByRole('button', { name: 'Go' }))
    expect(onClick).toHaveBeenCalledOnce()
  })

  it('does not fire when disabled', async () => {
    const onClick = vi.fn()
    const user = userEvent.setup()
    render(
      <Button onClick={onClick} disabled>
        Go
      </Button>,
    )

    await user.click(screen.getByRole('button', { name: 'Go' }))
    expect(onClick).not.toHaveBeenCalled()
  })

  it('renders as its child with asChild, keeping link semantics', () => {
    render(
      <Button asChild>
        <a href="/files">Browse</a>
      </Button>,
    )
    const link = screen.getByRole('link', { name: 'Browse' })
    expect(link).toHaveAttribute('href', '/files')
    // A link rendered through asChild must not gain a type attribute.
    expect(link).not.toHaveAttribute('type')
  })

  it('lets a caller-supplied class override a variant default', () => {
    render(<Button className="bg-danger">Delete</Button>)
    const cls = screen.getByRole('button', { name: 'Delete' }).className
    expect(cls).toContain('bg-danger')
    expect(cls).not.toContain('bg-primary')
  })
})

describe('Alert', () => {
  it('announces errors assertively', () => {
    render(
      <Alert tone="danger" title="Upload failed">
        The connection dropped.
      </Alert>,
    )
    const alert = screen.getByRole('alert')
    expect(alert).toHaveAttribute('aria-live', 'assertive')
    expect(alert).toHaveTextContent('Upload failed')
    expect(alert).toHaveTextContent('The connection dropped.')
  })

  it('announces non-errors politely so it does not interrupt', () => {
    render(<Alert tone="info">Nothing to see.</Alert>)
    expect(screen.getByRole('status')).toHaveAttribute('aria-live', 'polite')
  })

  it('renders an action', () => {
    render(
      <Alert tone="danger" action={<button type="button">Retry</button>}>
        Failed.
      </Alert>,
    )
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })
})

describe('Badge', () => {
  it('renders its content', () => {
    render(<Badge variant="success">Healthy</Badge>)
    expect(screen.getByText('Healthy')).toBeInTheDocument()
  })
})

describe('Card', () => {
  it('composes a heading and content with correct semantics', () => {
    render(
      <Card>
        <CardHeader>
          <CardTitle>Service health</CardTitle>
        </CardHeader>
        <CardContent>All good.</CardContent>
      </Card>,
    )
    expect(screen.getByRole('heading', { name: 'Service health' })).toBeInTheDocument()
    expect(screen.getByText('All good.')).toBeInTheDocument()
  })
})
