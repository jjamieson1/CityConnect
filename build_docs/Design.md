# Build instructions

## Description

City Connect is a CRM application that allows users to manage their contacts, track interactions, and analyze data to improve customer relationships. The application is built using modern web technologies and follows best practices for software development.

There are no user interfaces in this application, as it is designed to be used as a backend service. The application provides a RESTful API for interacting with the data and performing various operations.

There is a admin interface to review requests and manage the system, but it is not intended for end-users.

## Features

- Contact management: Add, edit, and delete contacts, as well as organize them into groups.
- Interaction tracking: Log interactions with contacts, including calls, emails, and meetings.
- Data analysis: Generate reports and insights based on contact interactions and engagement.
- Admin interface: Review requests and manage the system.
- Routing management, create and manage queues, and assign requests to agents.
- Agent management, invite users to act as agents, manage agents, and assign them to queues. Agents can also be connected systems to receive requests (for example, we will create a permitting application later on that will be connected to C2 to receive requests).
- API for incoming requests, including creating, updating, and retrieving requests from service cards in C2.
- Notification integration, including sending notifications to citizens C2 notification system.
- Callback service delivering status bundles of open tickets with ticket ID, status, and last updated timestamp and descriptions of actions.

## Running

Available on port 4021

## Database

The application uses a MariaDB database to store contact information, interaction logs, and other relevant data. The database schema is designed to support the application's features and ensure data integrity.
