<p>
<img width="480" height="140" alt="logo" src="https://github.com/user-attachments/assets/ecbacf7d-4bc3-451e-bcbb-dce7c9a731b0" />
</p>

# goletter

A small terminal UI for sending newsletters with Amazon SES.

Subscribers live in an SES contact list, content lives in plain files you can
keep in git, and every email goes out through the SES API with SES-managed
unsubscribe links. Nothing else to run or host.

<img width="360" height="233" alt="image" src="https://github.com/user-attachments/assets/020e32da-4397-47f9-b57d-c55e56f307e4" />

## Install

```sh
go build        # Go 1.27 or newer, produces ./goletter
```

You also need the [AWS CLI](https://aws.amazon.com/cli/) 2.32 or newer for
`aws login`: goletter keeps its own AWS profile (`goletter` by default) that
only ever holds a browser login session, so access keys or roles configured
elsewhere on the machine are never used.
Everything else is pure Go.

Open and click statistics per campaign come from Virtual Deliverability
Manager in SES, which AWS bills at $0.07 per 1,000 emails. goletter shows
whether it is on and can turn it on from the Account screen.

## Quick start

```sh
goletter <directory>
```
