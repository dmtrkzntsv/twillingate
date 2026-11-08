import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router'
import { ApiError, endpoints, type Project } from '@/lib/api'
import { form } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import NewFormDialog from './NewFormDialog'

const project: Project = { project_id: 4, name: 'shop', allowed_origins: ['https://shop.example'] }

beforeEach(() => {
  vi.restoreAllMocks()
})

function LocationProbe() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname + location.search}</output>
}

function renderDialog() {
  const onOpenChange = vi.fn()
  const out = renderWithProviders(
    <MemoryRouter initialEntries={['/projects/4/forms/contact?range=7d']}>
      <NewFormDialog open onOpenChange={onOpenChange} project={project} />
      <LocationProbe />
    </MemoryRouter>
  )
  return { ...out, onOpenChange, dialog: screen.getByRole('dialog', { name: 'New form' }) }
}

async function addField(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement, name: string) {
  await user.type(within(dialog).getByRole('textbox', { name: 'New field' }), name)
  await user.click(within(dialog).getByRole('button', { name: 'Add field' }))
}

describe('NewFormDialog', () => {
  it('checks the name as it is typed', async () => {
    const user = userEvent.setup()
    const { dialog } = renderDialog()
    const name = within(dialog).getByLabelText('Name')
    expect(within(dialog).getByText(/Lower-case letters, digits/)).toBeInTheDocument()
    await user.type(name, 'Contact Us')
    expect(name).toHaveAttribute('aria-invalid', 'true')
    expect(within(dialog).getByText('Use 1 to 64 lower-case letters, digits, _ and -.')).toBeInTheDocument()
    await addField(user, dialog, 'email')
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled()
    await user.clear(name)
    await user.type(name, 'contact-us')
    expect(name).not.toHaveAttribute('aria-invalid')
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeEnabled()
  })

  it('needs one field or more, none starting with $', async () => {
    const user = userEvent.setup()
    const { dialog } = renderDialog()
    await user.type(within(dialog).getByLabelText('Name'), 'lead')
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled()
    await user.type(within(dialog).getByRole('textbox', { name: 'New field' }), '$path')
    expect(within(dialog).getByText(/reserved for context keys/)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Add field' })).toBeDisabled()
    await user.clear(within(dialog).getByRole('textbox', { name: 'New field' }))
    await addField(user, dialog, 'email')
    await addField(user, dialog, 'company')
    expect(within(dialog).getByRole('checkbox', { name: 'email' })).toBeChecked()
    // Unchecking takes a field off.
    await user.click(within(dialog).getByRole('checkbox', { name: 'company' }))
    expect(within(dialog).queryByRole('checkbox', { name: 'company' })).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeEnabled()
  })

  it('names the allowed origins beside the return address', () => {
    const { dialog } = renderDialog()
    expect(within(dialog).getByText(/allowed origins: https:\/\/shop\.example\./)).toBeInTheDocument()
  })

  it('creates the form, closes and opens its tab, keeping the range', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(endpoints, 'createForm').mockResolvedValue(form('lead', { fields: ['email'], expected_fields: ['email'] }))
    const { dialog, onOpenChange } = renderDialog()
    await user.type(within(dialog).getByLabelText('Name'), 'lead')
    await user.type(within(dialog).getByLabelText('Purpose'), ' Sales leads ')
    await addField(user, dialog, 'email')
    await user.type(within(dialog).getByLabelText('Return address'), 'https://shop.example/thanks')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(4, { name: 'lead', purpose: 'Sales leads', expected_fields: ['email'], return_url: 'https://shop.example/thanks' }),
      { timeout: 5000 }
    )
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(screen.getByTestId('location')).toHaveTextContent('/projects/4/forms/lead?range=7d')
  })

  it('leaves out an empty purpose and return address', async () => {
    const user = userEvent.setup()
    const create = vi.spyOn(endpoints, 'createForm').mockResolvedValue(form('lead'))
    const { dialog } = renderDialog()
    await user.type(within(dialog).getByLabelText('Name'), 'lead')
    await addField(user, dialog, 'email')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(create).toHaveBeenCalledWith(4, { name: 'lead', expected_fields: ['email'] }))
  })

  it('says on the name when the project already has a form so named', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'createForm').mockRejectedValue(new ApiError(409, 'form contact already exists', 'conflict'))
    const { dialog, onOpenChange } = renderDialog()
    const name = within(dialog).getByLabelText('Name')
    await user.type(name, 'contact')
    await addField(user, dialog, 'email')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(await within(dialog).findByText('A form named contact already exists (it may be archived).')).toBeInTheDocument()
    expect(name).toHaveAttribute('aria-invalid', 'true')
    expect(onOpenChange).not.toHaveBeenCalled()
    // Create waits for another name rather than send the same one again.
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled()
    // Editing the name clears it.
    await user.type(name, '2')
    expect(within(dialog).queryByText(/already exists/)).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeEnabled()
  })

  it("shows any other refusal's message", async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'createForm').mockRejectedValue(new ApiError(400, 'return_url: origin https://else.example is not allowed', 'invalid'))
    const { dialog } = renderDialog()
    await user.type(within(dialog).getByLabelText('Name'), 'lead')
    await addField(user, dialog, 'email')
    await user.type(within(dialog).getByLabelText('Return address'), 'https://else.example/x')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('return_url: origin https://else.example is not allowed')
  })
})
