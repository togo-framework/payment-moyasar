# payment-moyasar

[Moyasar](https://docs.moyasar.com) driver for togo **payment**.

```bash
togo install togo-framework/payment
togo install togo-framework/payment-moyasar
```
```env
PAYMENT_DRIVER=moyasar
MOYASAR_SECRET_KEY=...
```

Registers on the togo `payment.PaymentProvider` interface and is selected via
`PAYMENT_DRIVER=moyasar`. Gateway API calls are scaffolded — see the Moyasar docs.

MIT
