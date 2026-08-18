# Running alborz with a Google account

## Create an application password

First, you'll need to obtain an application-specific password for alborz from
the [app passwords] page on your Google account.

## Run alborz

Start alborz with these upstream URLs:

    alborz imaps://imap.gmail.com smtps://smtp.gmail.com \
        carddavs://www.googleapis.com/carddav/v1/principals/YOUREMAIL/ \
        caldavs://www.google.com/calendar/dav

Replace `YOUREMAIL` with your Google account's e-mail address.

Once alborz is started, you can login with your e-mail address and the app
password.

[app passwords]: https://security.google.com/settings/security/apppasswords
