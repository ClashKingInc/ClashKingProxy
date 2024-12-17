FROM python:3.11-bookworm

LABEL org.opencontainers.image.source=https://github.com/ClashKingInc/ClashKingProxy
LABEL org.opencontainers.image.description="Image for the ClashKing API Proxy"
LABEL org.opencontainers.image.licenses=MIT

# Set the working directory in the container
WORKDIR /app

# First, copy only the requirements.txt file
COPY requirements.txt .

# Install any needed packages specified in requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

# Now copy the rest of the application code into the container
COPY . .

# Expose the port the app runs on
EXPOSE 80

# Command to run the FastAPI application with multiple workers
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8010", "--workers", "4"]