<?php

namespace App\Controller;

use Doctrine\DBAL\Connection;
use Psr\Log\LoggerInterface;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\Routing\Attribute\Route;

class ApiController
{
    public function __construct(
        private readonly Connection $db,
        private readonly LoggerInterface $logger,
    ) {
    }

    #[Route('/health', methods: ['GET'])]
    public function health(): JsonResponse
    {
        return new JsonResponse(['status' => 'ok', 'service' => 'symfony-demo']);
    }

    #[Route('/api/hello', methods: ['GET'])]
    public function hello(): JsonResponse
    {
        $count = (int) $this->db->fetchOne('SELECT COUNT(*) FROM visits');
        $this->logger->info('symfony hello', ['visits' => $count]);

        return new JsonResponse([
            'service' => 'symfony-demo',
            'message' => 'hello from frankenphp+symfony',
            'visits' => $count,
        ]);
    }

    #[Route('/api/visits', methods: ['GET'])]
    public function visits(): JsonResponse
    {
        $this->db->insert('visits', ['path' => '/api/visits']);
        $count = (int) $this->db->fetchOne('SELECT COUNT(*) FROM visits');
        $this->logger->info('symfony visit recorded', ['visits' => $count]);

        return new JsonResponse([
            'service' => 'symfony-demo',
            'recorded' => true,
            'visits' => $count,
        ]);
    }

    #[Route('/api/slow', methods: ['GET'])]
    public function slow(): JsonResponse
    {
        $this->db->executeQuery('SELECT pg_sleep(0.25)');

        return new JsonResponse(['service' => 'symfony-demo', 'slow' => true]);
    }

    #[Route('/api/error', methods: ['GET'])]
    public function error(): JsonResponse
    {
        $this->logger->error('symfony simulated failure');

        return new JsonResponse(['service' => 'symfony-demo', 'error' => 'simulated'], 500);
    }
}
