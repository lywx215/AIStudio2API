import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import vm from 'node:vm'

// Exercise the actual code sent to Camoufox against the two observed pages.
const source = readFileSync(
  new URL('../../internal/camoufoxnative/login_onboarding.go', import.meta.url),
  'utf8',
)
const production = source.match(/const loginOnboardingFunction = `([\s\S]*?)`/)[1]
const emailSource = readFileSync(
  new URL('../../internal/camoufoxnative/login.go', import.meta.url),
  'utf8',
).match(/const loginEmailExpression = `([\s\S]*?)`/)[1]
const requiredLabel =
  'I acknowledge that I am at least 18 years old, and that I am a developer using the service for professional or business purposes to build products for users 18+.'
const marketingLabel = "I'd like to receive emails about model updates, offers and research."

function page({ authorized = true, stage = 'terms', delayed = false } = {}) {
  const clicks = []
  const listeners = {}
  const body = { innerText: '' }
  const element = (name, label, attributes = {}) => ({
    name,
    innerText: label,
    labels: [{ innerText: label }],
    checked: false,
    disabled: false,
    hidden: false,
    attributes,
    getAttribute(key) {
      return this.attributes[key] ?? null
    },
    getClientRects() {
      return this.hidden ? [] : [{}]
    },
    closest() {
      return null
    },
    click() {
      clicks.push(name)
      if (!delayed && (name === 'required' || name === 'marketing')) this.checked = !this.checked
    },
  })
  const required = element('required', requiredLabel)
  const marketing = element('marketing', marketingLabel)
  const next = element('continue', 'Continue')
  const skip = element('skip', 'Skip')
  const choice = element('choice', 'Explore the latest models')
  let boxes = [required, marketing]
  let buttons = [next]
  const identities = []
  const location = { origin: 'https://aistudio.google.com', pathname: '/onboarding' }
  const context = vm.createContext({
    location,
    window: {},
    getComputedStyle: (e) => ({ visibility: e.visibility ?? 'visible' }),
    document: {
      body,
      addEventListener: (name, listener) => (listeners[name] = listener),
      getElementById: () => null,
      querySelectorAll(selector) {
        if (selector === 'input[type="checkbox"],[role="checkbox"]') return boxes
        if (selector === 'button,[role="button"],a') return buttons
        if (selector === '[data-email],[data-identifier]') return identities
        if (selector === '[aria-label],[title],[data-email]') return identities
        throw new Error('Unexpected selector in fixture')
      },
    },
  })
  const execute = vm.runInContext(`(${production})`, context)
  const setStage = (value) => {
    body.innerText =
      value === 'terms'
        ? 'Welcome to Google AI Studio'
        : value === 'preferences'
          ? 'What brings you to Google AI Studio?'
          : 'Loading'
    boxes = value === 'terms' ? [required, marketing] : []
    buttons = value === 'terms' ? [next] : [choice, skip]
  }
  setStage(stage)
  return {
    clicks,
    listeners,
    required,
    marketing,
    next,
    skip,
    body,
    context,
    location,
    identities,
    element,
    setStage,
    setBoxes: (value) => (boxes = value),
    setButtons: (value) => (buttons = value),
    action: (kind) => execute(kind, authorized, 'user@example.test'),
    email: () => vm.runInContext(emailSource, context),
  }
}

test('first-use agreement requires a boolean permission for this login', () => {
  for (const authorized of [false, null, 'true', 1]) {
    const p = page({ authorized })
    assert.equal(p.action('inspect'), 'consent_required')
    assert.equal(p.action('onboarding_terms'), 'consent_required')
    assert.deepEqual(p.clicks, [])
    assert.equal(p.required.checked, false)
    assert.equal(p.marketing.checked, false)
  }
})

test('authorized two-step flow accepts only required terms and skips the survey on the same URL', () => {
  const p = page()
  assert.equal(p.action('inspect'), 'onboarding_terms')
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  assert.equal(p.required.checked, true)
  assert.equal(p.marketing.checked, false)
  assert.equal(p.action('onboarding_terms'), 'submitted')
  assert.equal(p.action('onboarding_terms'), 'onboarding_pending')
  p.setStage('preferences')
  assert.equal(p.location.pathname, '/onboarding')
  assert.equal(p.action('inspect'), 'onboarding_preferences')
  assert.equal(p.action('onboarding_preferences'), 'submitted')
  assert.equal(p.action('onboarding_preferences'), 'onboarding_pending')
  assert.deepEqual(p.clicks, ['required', 'continue', 'skip'])
  assert.equal(p.marketing.checked, false)
})

test('preselected emails are turned off before accepting; an accepted required checkbox stays on', () => {
  const p = page()
  p.marketing.checked = true
  p.required.checked = true
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  assert.equal(p.action('onboarding_terms'), 'submitted')
  assert.deepEqual(p.clicks, ['marketing', 'continue'])
  assert.equal(p.marketing.checked, false)
  assert.equal(p.required.checked, true)
})

test('real AI Studio Material arrow ligatures do not obscure Continue or Skip', () => {
  const p = page()
  p.next.innerText = ' Continue\narrow_forward '
  assert.equal(p.action('inspect'), 'onboarding_terms')
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  assert.equal(p.action('onboarding_terms'), 'submitted')
  p.setStage('preferences')
  p.skip.innerText = 'Skip\narrow_forward'
  assert.equal(p.action('onboarding_preferences'), 'submitted')
  assert.deepEqual(p.clicks, ['required', 'continue', 'skip'])
  assert.equal(p.marketing.checked, false)
})

test('decorative icon handling does not accept other buttons with additional words', () => {
  const p = page()
  p.next.innerText = 'Continue to another service arrow_forward'
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  assert.deepEqual(p.clicks, [])
  p.setStage('preferences')
  p.skip.innerText = 'Skip to main content arrow_forward'
  assert.equal(p.action('onboarding_preferences'), 'onboarding_loading')
  assert.deepEqual(p.clicks, [])
})

test('delayed checkbox updates and disabled Continue do not toggle or submit twice', () => {
  const p = page({ delayed: true })
  p.marketing.checked = true
  p.next.disabled = true
  p.action('onboarding_terms')
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  assert.deepEqual(p.clicks, ['marketing'])
  p.marketing.checked = false
  p.action('onboarding_terms')
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  assert.deepEqual(p.clicks, ['marketing', 'required'])
  p.required.checked = true
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  p.next.disabled = false
  assert.equal(p.action('onboarding_terms'), 'submitted')
  p.action('onboarding_terms')
  assert.deepEqual(p.clicks, ['marketing', 'required', 'continue'])
})

test('a login already on the optional survey only skips and needs no new agreement permission', () => {
  const p = page({ stage: 'preferences', authorized: false })
  assert.equal(p.action('onboarding_preferences'), 'submitted')
  assert.deepEqual(p.clicks, ['skip'])
})

test('missing controls wait for rendering; hidden duplicates are ignored and visible duplicates stop', () => {
  const p = page()
  p.setBoxes([p.required])
  assert.equal(p.action('onboarding_terms'), 'onboarding_loading')
  p.setBoxes([p.required, p.marketing])
  const duplicate = p.element('duplicate', requiredLabel)
  duplicate.hidden = true
  p.setBoxes([p.required, p.marketing, duplicate])
  assert.equal(p.action('inspect'), 'onboarding_terms')
  duplicate.hidden = false
  assert.equal(p.action('onboarding_terms'), 'manual')
  assert.deepEqual(p.clicks, [])
})

test('survey transitions, missing Skip, disabled Skip and duplicate Skip never choose a use case', () => {
  const p = page({ stage: 'preferences' })
  p.body.innerText += ' Welcome to Google AI Studio'
  assert.equal(p.action('onboarding_preferences'), 'onboarding_loading')
  p.setStage('preferences')
  p.setButtons([])
  assert.equal(p.action('onboarding_preferences'), 'onboarding_loading')
  p.setButtons([p.skip])
  p.skip.disabled = true
  assert.equal(p.action('onboarding_preferences'), 'onboarding_loading')
  p.skip.disabled = false
  p.setButtons([p.skip, p.element('other-skip', 'Skip')])
  assert.equal(p.action('onboarding_preferences'), 'manual')
  assert.deepEqual(p.clicks, [])
})

test('only the exact HTTPS AI Studio onboarding page can accept terms', () => {
  for (const origin of ['http://aistudio.google.com', 'https://aistudio.google.com.example.test']) {
    const p = page()
    p.location.origin = origin
    assert.equal(p.action('onboarding_terms'), 'not_onboarding')
    assert.deepEqual(p.clicks, [])
  }
  const p = page()
  p.location.pathname = '/prompts/new_chat'
  assert.equal(p.action('onboarding_terms'), 'aistudio_loading')
  assert.deepEqual(p.clicks, [])
})

test('a different displayed account or trusted user input stops agreement automation', () => {
  const p = page()
  p.identities.push(p.element('account', '', { 'data-email': 'other@example.test' }))
  assert.equal(p.action('onboarding_terms'), 'manual')
  assert.deepEqual(p.clicks, [])
  const manual = page()
  manual.action('inspect')
  manual.listeners.input({ isTrusted: false })
  assert.equal(manual.action('inspect'), 'onboarding_terms')
  manual.listeners.pointerdown({ isTrusted: true })
  assert.equal(manual.action('onboarding_terms'), 'manual')
  assert.deepEqual(manual.clicks, [])
})

test('inspection and diagnostics never change checkboxes, accept terms or select survey choices', () => {
  for (const stage of ['terms', 'preferences', 'loading']) {
    const p = page({ stage })
    p.marketing.checked = true
    p.action('inspect')
    assert.match(p.action('diagnose'), /^diag_onboarding_[a-z_]+$/)
    assert.deepEqual(p.clicks, [])
    assert.equal(p.marketing.checked, true)
    assert.equal(p.required.checked, false)
  }
})

test('email detection waits until onboarding finishes and prefers account metadata over page examples', () => {
  const p = page()
  p.identities.push(p.element('example', '', { title: 'A sample: sample@example.test' }))
  p.identities.push(p.element('account', '', { title: 'Google Account: user@example.test' }))
  p.body.innerText = 'An example address is sample@example.test'
  assert.equal(p.email(), '')
  p.location.pathname = '/prompts/new_chat'
  assert.equal(p.email(), 'user@example.test')
  p.location.origin = 'https://example.test'
  assert.equal(p.email(), '')
})

test('email detection rejects ambiguous identities instead of taking the first email', () => {
  const p = page()
  p.location.pathname = '/prompts/new_chat'
  p.body.innerText = 'user@example.test other@example.test'
  assert.equal(p.email(), '')
  p.identities.push(p.element('account', '', { 'data-email': 'user@example.test' }))
  assert.equal(p.email(), 'user@example.test')
  p.identities.push(p.element('other', '', { 'data-email': 'other@example.test' }))
  assert.equal(p.email(), '')
})
