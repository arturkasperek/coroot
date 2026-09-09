<?php

namespace App\Command;

use Doctrine\DBAL\Connection;
use Symfony\Component\Console\Attribute\AsCommand;
use Symfony\Component\Console\Command\Command;
use Symfony\Component\Console\Input\InputInterface;
use Symfony\Component\Console\Output\OutputInterface;

#[AsCommand(name: 'app:init-db', description: 'Create the visits table if it does not exist')]
class InitDbCommand extends Command
{
    public function __construct(private readonly Connection $db)
    {
        parent::__construct();
    }

    protected function execute(InputInterface $input, OutputInterface $output): int
    {
        $this->db->executeStatement(<<<'SQL'
            CREATE TABLE IF NOT EXISTS visits (
                id SERIAL PRIMARY KEY,
                path TEXT NOT NULL,
                created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
            )
            SQL);

        $output->writeln('visits table ready');

        return Command::SUCCESS;
    }
}
