#include <stdio.h>
#include <signal.h>
#include <unistd.h>

static int quit = 0;

void sig_handler(int signo)
{
  printf("received %d\n", signo);
  quit = 1;
}

int main(void)
{
  if (signal(SIGINT, sig_handler) == SIG_ERR)
    printf("\ncan't catch SIGINT\n");
  if (signal(SIGTERM, sig_handler) == SIG_ERR)
    printf("\ncan't catch SIGTERM\n");
  // A long long wait so that we can easily issue a signal to this process
  while(!quit)
    sleep(1);
  return 0;
}
