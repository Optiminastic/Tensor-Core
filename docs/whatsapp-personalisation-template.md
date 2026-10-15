# The personalisation-check WhatsApp template

What to submit in WhatsApp Manager, and why each part is the way it is.

Tensor does not hold this copy. `internal/integrations/whatsapp/template.go`
explains the rule: the sentences live at Meta, the code supplies parameters, and
the log is rendered by reading the approved template back. So this file is a
submission brief, not a second source of truth — once it is approved, the words
at Meta win.

## Category: UTILITY, never MARKETING

This is the single decision that makes or breaks the feature.

India applies a per-user **marketing-template cap**: once a recipient has had
their quota of marketing messages in a period, further marketing templates are
accepted by the API — `messages: [{ id: ... }]`, HTTP 200, no error anywhere —
and never delivered. We have already measured exactly that on this WABA with
the win-back template.

A personalisation check is a transactional message about an order the customer
has just paid for, sent once, carrying a link they must act on inside thirty
minutes. That is what UTILITY is for. Submitted as MARKETING it will silently
not arrive for the customers who need it most — the repeat buyers.

If Meta re-categorises it to MARKETING after approval, **stop sending and fix
the copy** rather than shipping around it. The usual cause is a sentence that
reads as an offer; keep every line about *this order*.

## Shape

Three body parameters and one URL-button parameter, in this order. The order is
fixed by `personalisationBodyParams` (`internal/httpapi/personalisation_rules.go:103`)
and the `ButtonParams` at `personalisation_worker.go:322`.

| Where | Placeholder | Tensor sends | Example |
| --- | --- | --- | --- |
| Body | `{{1}}` | Customer name, or `there` when the order has none | `Priya` |
| Body | `{{2}}` | Order number, or `your order` | `#1043` |
| Body | `{{3}}` | How many personalised **units** are on the order | `2` |
| Button | `{{1}}` | The path only: `p/<token>` | `p/8f3c...` |

None of them may ever be blank — Meta refuses an empty parameter and names
neither the field nor the position, which is why the client checks first and
why both fallbacks above exist.

### Header

None. A header would need its own approval round and carries nothing the body
does not already say.

### Body

```
Hi {{1}}, thanks for your order {{2}}.

Before we start printing, please check the spelling and colours on your {{3}} personalised item(s). You have 30 minutes to make changes — after that your order goes straight to production.
```

Notes on the wording, each of which is load-bearing:

- **The thirty minutes is stated.** The page shows a live countdown, but the
  customer decides whether to open the link at all from this message.
- **"goes straight to production"** says what happens on silence. Doing nothing
  is a valid choice and the message must not imply the order is stuck.
- **No discount, no offer, no second product.** Anything of that kind makes this
  MARKETING, with the delivery consequence above.
- `{{3}}` is a count of **units**, not lines: three of the same plank is three
  things to check, and "1 item" about three planks reads as though two were
  lost.

### Footer

```
Tensor3D — reply STOP to opt out of order updates
```

### Button: one, Call-to-action → Visit website → Dynamic

- **Text**: `Check my order`
- **URL type**: Dynamic
- **URL**: `https://<FRONTEND_URL host>/{{1}}`
- **Sample**: `p/abc123`

Dynamic, and the **variable is the whole path after the host** — Tensor sends
`p/<token>`, not the bare token, so the base URL must end at the host with no
`/p/` of its own. Get this wrong in either direction and every link 404s while
the send reports success.

The host must match `FRONTEND_URL` for the environment that sends. A template
approved against the production domain cannot be used to test against
`localhost`; for local testing, read the link out of the
`personalisation_links` row instead of clicking the button.

## Wiring it up once approved

```sh
PERSONALISATION_TEMPLATE_NAME=personalisation_check   # the approved name
WHATSAPP_TEMPLATE_LANGUAGE=en                         # must match exactly; "en" and "en_US" are different templates
FRONTEND_URL=https://<the public address>             # without it the sender logs
                                                      # "no public address is configured" and sends nothing
```

`PERSONALISATION_TEMPLATE_NAME` defaults to empty, and an empty name falls back
to `WHATSAPP_TEMPLATE_NAME` — today the win-back template, which has different
parameters. So leaving it unset does not disable the feature; it sends the
wrong template with the wrong arguments.

**`cmd/productionworker` needs all three too.** `whatsappFor` starts from the
environment and only then overlays per-brand rows from `integration_settings`:
the DB half is shared between the API and the worker, the env half is not.

## Testing it without messaging a customer

`PERSONALISATION_TEST_RECIPIENT` redirects every send to one number, and while
it is set no customer can be messaged however the other switches are turned.
The link row then records `sent to the test recipient, not the customer`, so the
log never reads like a real send.

Turn `PERSONALISATION_CHECK_ENABLED=true` on only with that set, and narrow it
further with `PERSONALISATION_ONLY` to a single order number for the first run.
